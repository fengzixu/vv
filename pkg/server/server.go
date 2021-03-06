package server

import (
	"bytes"
	"github.com/fsnotify/fsnotify"
	"github.com/kr/binarydist"
	"github.com/pkg/errors"
	"github.com/wangkechun/vv/pkg/editor"
	"github.com/wangkechun/vv/pkg/header"
	pb "github.com/wangkechun/vv/pkg/proto"
	context "golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"qiniupkg.com/x/log.v7"
	"time"
)

const splitFileSize = 1024 * 100
const defaultConnectionNum = 1

// server is used to implement helloworld.GreeterServer.
type fileServer struct {
	name string
}

// SayHello implements helloworld.GreeterServer
func (s *fileServer) Ping(ctx context.Context, in *pb.PingRequest) (out *pb.PingReply, err error) {
	log.Info("recv Ping")
	return &pb.PingReply{Name: s.name}, nil
}
func (s *fileServer) OpenFileStream(stream pb.VvServer_OpenFileStreamServer) (err error) {
	log.Info("recv OpenFileStream")
	// TODO:更好的处理文件重名问题
	msg, err := stream.Recv()
	if err != nil {
		return errors.Wrap(err, "stream.Recv")
	}
	fileName := filepath.Join(os.TempDir(), msg.FileName)
	fileWriter, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return errors.Wrap(err, "os.OpenFile")
	}
	defer fileWriter.Close()
	_, err = fileWriter.Write(msg.Content)
	if err != nil {
		return errors.Wrap(err, "fileWriter.Write")
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return errors.Wrap(err, "stream.Recv")
		}
		if msg.IsEnd {
			break
		}
		_, err = fileWriter.Write(msg.Content)
		if err != nil {
			return errors.Wrap(err, "fileWriter.Write")
		}
	}
	fileWriter.Close()
	log.Info("recv split file success")

	defer os.Remove(fileName)
	content, err := ioutil.ReadFile(fileName)
	if err != nil {
		return errors.Wrap(err, "ioutil.ReadFile")
	}
	s.openAndWatchFile(fileName, stream, content)
	return
}

func (s *fileServer) openAndWatchFile(fileName string, stream pb.VvServer_OpenFileServer, content []byte) (err error) {
	command := editor.Cmd(fileName)
	log.Info("run", command)

	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	err = cmd.Start()

	if err != nil {
		return errors.Wrap(err, "open editor error")
	}

	go func() {
		cmd.Wait()
		log.Info("close editor")
		cancel()
	}()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return errors.Wrap(err, "fsnotify.NewWatcher")
	}
	watcher.Add(fileName)
	defer watcher.Close()

	lastContnet := content

	for {
		select {
		case <-ctx.Done():
			return grpc.Errorf(codes.Aborted, "editor or client closed")
		case event := <-watcher.Events:
			if event.Op&fsnotify.Write == fsnotify.Write {
				log.Println("modified file:", event.Name)
				// TODO: bsdiff 支持
				buf, err := ioutil.ReadFile(fileName)
				if err != nil {
					return errors.Wrap(err, "read file error")
				}
				if bytes.Compare(lastContnet, buf) == 0 {
					continue
				}
				if len(buf) > splitFileSize {
					// TODO: 大文件bsdiff很慢
					patch := &bytes.Buffer{}
					err = binarydist.Diff(bytes.NewReader(lastContnet), bytes.NewReader(buf), patch)
					if err != nil {
						return errors.Wrap(err, "binarydist.Diff")
					}
					log.Infof("server, file %s change , push diff, diff size: %d", fileName, len(patch.Bytes()))
					err = stream.Send(&pb.OpenFileReply{
						Content:  patch.Bytes(),
						IsBsdiff: true,
					})
					if err != nil {
						log.Error("send file error", err)
						return err
					}
				} else {
					lastContnet = buf
					log.Infof("server, file %s change , push it", fileName)
					err = stream.Send(&pb.OpenFileReply{
						Content: lastContnet,
					})
					if err != nil {
						log.Error("send file error", err)
						return err
					}
				}

			}
		case err := <-watcher.Errors:
			log.Error("watch file error", err)
			return err
		}
	}
}

func (s *fileServer) OpenFile(in *pb.OpenFileRequest, stream pb.VvServer_OpenFileServer) (err error) {
	log.Info("recv OpenFile")
	// TODO:更好的处理文件重名问题
	fileName := filepath.Join(os.TempDir(), in.FileName)
	err = ioutil.WriteFile(fileName, in.Content, 0600)
	if err != nil {
		return errors.Wrap(err, "write file")
	}
	defer os.Remove(fileName)

	log.Info("write file", fileName)
	return s.openAndWatchFile(fileName, stream, in.Content)
}

// Server 组件
type Server struct {
	cfg Config
}

// Config Server 配置
type Config struct {
	RegistryAddrRPC string
	RegistryAddrTCP string
	Name            string
}

// New 返回一个Server
func New(cfg Config) *Server {
	r := &Server{cfg: cfg}
	return r
}

// Run 启动
func (r *Server) Run() (err error) {
	ctx := context.Background()
	for {
		func() error {
			log.Info("connecting to", r.cfg.RegistryAddrRPC)
			conn, err := grpc.Dial(r.cfg.RegistryAddrRPC, grpc.WithInsecure())
			if err != nil {
				return errors.Wrap(err, "dial error")
			}
			defer conn.Close()
			c := pb.NewVvRegistryClient(conn)
			stream, err := c.OpenListen(ctx, &pb.OpenListenRequest{User: r.cfg.Name})
			if err != nil {
				return errors.Wrap(err, "OpenListen")
			}
			for {
				log.Info("connecting success")
				_, err := stream.Recv()
				if err != nil {
					return errors.Wrap(err, "stream.Recv")
				}
				srv := &fileServer{
					name: r.cfg.Name,
				}
				s := grpc.NewServer()
				pb.RegisterVvServerServer(s, srv)
				reflection.Register(s)
				log.Info("dial", r.cfg.RegistryAddrTCP)
				conn, err := net.Dial("tcp", r.cfg.RegistryAddrTCP)
				if err != nil {
					return errors.Wrap(err, "net.Dial")
				}
				header.WriteHeader(conn, &pb.ProtoHeader{
					ConnKind: pb.ProtoHeader_LISTEN,
					User:     r.cfg.Name,
				})
				go func() {
					err = s.Serve(&listen{conn: conn})
					if err != nil {
						log.Error(err)
					}
				}()
			}
		}()
		if err != nil {
			log.Error("dial error", err)
			time.Sleep(time.Second)
			continue
		}
	}
}
