package cmd

import (
	"github.com/spf13/cobra"
	"github.com/wangkechun/vv/pkg/registry"
)

var registerCmd = &cobra.Command{
	Use: "register",
}

var registerStartCmd = &cobra.Command{
	Use: "start",
	RunE: func(cmd *cobra.Command, args []string) error {
		registerStartCmdCfg.RegistryAddrRPC = "127.0.0.1:6655"
		registerStartCmdCfg.RegistryAddrTCP = "127.0.0.1:6656"
		return registry.New(registerStartCmdCfg.Config).Run()
	},
}
var registerStartCmdCfg struct {
	registry.Config
}

func init() {
	registerCmd.AddCommand(registerStartCmd)
	RootCmd.AddCommand(registerCmd)
}
