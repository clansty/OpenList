package qqgroup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

const defaultBusid = 102

// upload 以 base64:// 的形式调用 upload_group_file。
// OneBot 只能把文件内容放进 JSON body，所以这里手工拼 body 并用 Pipe 边编码边发，
// 避免把整个文件读进内存，同时让进度上报和取消（ctx）照常工作
func (d *QQGroup) upload(ctx context.Context, name, folder string, file model.FileStreamer, up driver.UpdateProgress) (string, error) {
	groupJson, err := json.Marshal(d.GroupID)
	if err != nil {
		return "", err
	}
	nameJson, err := json.Marshal(name)
	if err != nil {
		return "", err
	}
	folderJson, err := json.Marshal(folder)
	if err != nil {
		return "", err
	}
	prefix := []byte(`{"group_id":` + string(groupJson) + `,"file":"base64://`)
	suffix := []byte(`","name":` + string(nameJson) + `,"folder":` + string(folderJson) + `}`)

	pr, pw := io.Pipe()
	go func() {
		if _, err := pw.Write(prefix); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		src := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
			Reader:         &driver.SimpleReaderWithSize{Reader: file, Size: file.GetSize()},
			UpdateProgress: up,
		})
		enc := base64.NewEncoder(base64.StdEncoding, pw)
		if _, err := io.Copy(enc, src); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := enc.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := pw.Write(suffix); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.CloseWithError(nil)
	}()
	// 请求提前结束时（服务端报错、用户取消）让编码协程尽快退出
	defer pr.CloseWithError(context.Canceled)

	resp, err := d.uploadClient.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(pr).
		SetDoNotParseResponse(true).
		Post(d.actionURL("upload_group_file"))
	if err != nil {
		return "", err
	}
	body := resp.RawBody()
	defer body.Close()
	if resp.StatusCode() != 200 {
		msg, _ := io.ReadAll(io.LimitReader(body, 4096))
		return "", fmt.Errorf("上传 %s 失败（HTTP %d）: %s", name, resp.StatusCode(), string(msg))
	}
	var r apiResp
	if err = json.NewDecoder(body).Decode(&r); err != nil {
		return "", fmt.Errorf("解析 upload_group_file 的响应失败: %w", err)
	}
	if r.Retcode != 0 {
		msg := r.Wording
		if msg == "" {
			msg = r.Message
		}
		return "", fmt.Errorf("上传 %s 失败（retcode=%d）: %s", name, r.Retcode, msg)
	}
	var data struct {
		FileID string `json:"file_id"`
	}
	if len(r.Data) > 0 && string(r.Data) != "null" {
		_ = json.Unmarshal(r.Data, &data)
	}
	up(100)
	return data.FileID, nil
}
