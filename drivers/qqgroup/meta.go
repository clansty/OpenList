package qqgroup

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	GroupID int64  `json:"group_id" type:"number" required:"true" help:"要挂载的群，填群号"`
	APIURL  string `json:"api_url" required:"true" help:"OneBot 11 HTTP API 地址，例如 http://127.0.0.1:3000"`
	Token   string `json:"token" help:"OneBot HTTP API 的访问令牌，API 端没设置就留空"`
}

var config = driver.Config{
	Name:        "QQ Group File",
	LocalSort:   true,
	DefaultRoot: "/",
	CheckStatus: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &QQGroup{}
	})
}
