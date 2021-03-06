package header

import (
	"github.com/stretchr/testify/assert"
	pb "github.com/wangkechun/vv/pkg/proto"
	"net"
	"testing"
)

func TestReadWriteHeader(t *testing.T) {
	assert := assert.New(t)
	server, client := net.Pipe()
	go WriteHeader(client, &pb.ProtoHeader{
		Version:    "1",
		User:       "123",
		ServerKind: pb.ProtoHeader_CLIENT,
		ConnKind:   pb.ProtoHeader_DIAL,
	})
	header, err := ReadHeader(server)
	assert.Nil(err)
	assert.Equal(header.User, "123")
}
