package qqgroup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
)

func (d *QQGroup) actionURL(action string) string {
	return d.baseURL + "/" + action
}

// call 调一个 OneBot action，返回 data 原文
func (d *QQGroup) call(ctx context.Context, action string, payload base.Json) (json.RawMessage, error) {
	resp, err := d.client.R().
		SetContext(ctx).
		SetBody(payload).
		Post(d.actionURL(action))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode() == http.StatusUnauthorized || resp.StatusCode() == http.StatusForbidden {
		return nil, errs.NewErr(errs.NotSupport, "OneBot API 鉴权失败，请检查令牌")
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("请求 %s 失败（HTTP %d）: %s", action, resp.StatusCode(), strings.TrimSpace(resp.String()))
	}
	var r apiResp
	if err = json.Unmarshal(resp.Body(), &r); err != nil {
		return nil, fmt.Errorf("解析 %s 的响应失败: %w", action, err)
	}
	if r.Retcode != 0 {
		msg := r.Wording
		if msg == "" {
			msg = r.Message
		}
		if msg == "" {
			msg = r.Status
		}
		return nil, fmt.Errorf("%s 失败（retcode=%d）: %s", action, r.Retcode, msg)
	}
	return r.Data, nil
}

// callInto 调 action 并把 data 反序列化到 out，out 为 nil 时忽略返回值
func (d *QQGroup) callInto(ctx context.Context, action string, payload base.Json, out any) error {
	data, err := d.call(ctx, action, payload)
	if err != nil {
		return err
	}
	if out == nil || len(data) == 0 || string(data) == "null" {
		return nil
	}
	return json.Unmarshal(data, out)
}
