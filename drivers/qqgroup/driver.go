package qqgroup

import (
	"context"
	"fmt"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

// QQGroup 通过 OneBot 11 HTTP API 操作某个群的群文件，一个存储实例对应一个群
type QQGroup struct {
	model.Storage
	Addition
	baseURL string
	client  *resty.Client
	// 上传要流式发大 body，不能继承默认的超时和重试
	uploadClient *resty.Client
}

func (d *QQGroup) Config() driver.Config {
	return config
}

func (d *QQGroup) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *QQGroup) Init(ctx context.Context) error {
	if d.GroupID <= 0 {
		return fmt.Errorf("群号不能为空")
	}
	d.baseURL = strings.TrimRight(strings.TrimSpace(d.APIURL), "/")
	if d.baseURL == "" {
		return fmt.Errorf("OneBot API 地址不能为空")
	}
	d.client = base.NewRestyClient()
	d.uploadClient = base.NewRestyClient().SetTimeout(0).SetRetryCount(0)
	if d.Token != "" {
		auth := "Bearer " + d.Token
		d.client.SetHeader("Authorization", auth)
		d.uploadClient.SetHeader("Authorization", auth)
	}
	// 用群文件空间信息探活，顺便验证群号和令牌是否可用
	if _, err := d.getFileSystemInfo(ctx); err != nil {
		return err
	}
	return nil
}

func (d *QQGroup) Drop(ctx context.Context) error {
	return nil
}

func (d *QQGroup) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return d.list(ctx, dir.GetID(), dir.GetPath())
}

func (d *QQGroup) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	var resp struct {
		URL string `json:"url"`
	}
	err := d.callInto(ctx, "get_group_file_url", base.Json{
		"group_id": d.GroupID,
		"file_id":  file.GetID(),
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &model.Link{URL: resp.URL}, nil
}

func (d *QQGroup) GetRoot(ctx context.Context) (model.Obj, error) {
	return &model.Object{
		ID:       d.rootID(),
		Path:     "/",
		Name:     "root",
		Modified: d.Modified,
		IsFolder: true,
	}, nil
}

func (d *QQGroup) Get(ctx context.Context, path string) (model.Obj, error) {
	path = utils.FixAndCleanPath(path)
	if utils.PathEqual(path, "/") {
		return d.GetRoot(ctx)
	}
	var found model.Obj
	dirID, dirPath := d.rootID(), "/"
	for _, name := range strings.Split(strings.Trim(path, "/"), "/") {
		objs, err := d.list(ctx, dirID, dirPath)
		if err != nil {
			return nil, err
		}
		found = nil
		for _, obj := range objs {
			if obj.GetName() == name {
				found = obj
				break
			}
		}
		if found == nil {
			return nil, errs.NewErr(errs.ObjectNotFound, "群文件里没有 %s", name)
		}
		dirID, dirPath = found.GetID(), found.GetPath()
	}
	return found, nil
}

func (d *QQGroup) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	// OneBot 的 create_group_file_folder 只能建在根目录
	if !d.isRootID(parentDir.GetID()) {
		return nil, errs.NewErr(errs.NotSupport, "QQ 群文件只能在根目录下创建文件夹")
	}
	var resp struct {
		FolderID string `json:"folder_id"`
	}
	err := d.callInto(ctx, "create_group_file_folder", base.Json{
		"group_id": d.GroupID,
		"name":     dirName,
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &model.Object{
		ID:       resp.FolderID,
		Path:     stdpath.Join(parentDir.GetPath(), dirName),
		Name:     dirName,
		Modified: time.Now(),
		IsFolder: true,
	}, nil
}

func (d *QQGroup) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if srcObj.IsDir() {
		return nil, errs.NewErr(errs.NotSupport, "QQ 群文件不支持移动文件夹")
	}
	parentID, err := d.dirIDByPath(ctx, stdpath.Dir(srcObj.GetPath()))
	if err != nil {
		return nil, err
	}
	err = d.callInto(ctx, "move_group_file", base.Json{
		"group_id":         d.GroupID,
		"file_id":          srcObj.GetID(),
		"parent_directory": parentID,
		"target_directory": d.normDirID(dstDir.GetID()),
	}, nil)
	if err != nil {
		return nil, err
	}
	return &model.Object{
		ID:       srcObj.GetID(),
		Path:     stdpath.Join(dstDir.GetPath(), srcObj.GetName()),
		Name:     srcObj.GetName(),
		Size:     srcObj.GetSize(),
		Modified: srcObj.ModTime(),
		Ctime:    srcObj.CreateTime(),
	}, nil
}

func (d *QQGroup) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	var err error
	if srcObj.IsDir() {
		err = d.callInto(ctx, "rename_group_file_folder", base.Json{
			"group_id":        d.GroupID,
			"folder_id":       srcObj.GetID(),
			"new_folder_name": newName,
		}, nil)
	} else {
		var parentID string
		parentID, err = d.dirIDByPath(ctx, stdpath.Dir(srcObj.GetPath()))
		if err != nil {
			return nil, err
		}
		err = d.callInto(ctx, "rename_group_file", base.Json{
			"group_id":                 d.GroupID,
			"file_id":                  srcObj.GetID(),
			"current_parent_directory": parentID,
			"new_name":                 newName,
		}, nil)
	}
	if err != nil {
		return nil, err
	}
	return &model.Object{
		ID:       srcObj.GetID(),
		Path:     stdpath.Join(stdpath.Dir(srcObj.GetPath()), newName),
		Name:     newName,
		Size:     srcObj.GetSize(),
		Modified: srcObj.ModTime(),
		Ctime:    srcObj.CreateTime(),
		IsFolder: srcObj.IsDir(),
	}, nil
}

func (d *QQGroup) Remove(ctx context.Context, obj model.Obj) error {
	if obj.IsDir() {
		return d.callInto(ctx, "delete_group_folder", base.Json{
			"group_id":  d.GroupID,
			"folder_id": obj.GetID(),
		}, nil)
	}
	return d.callInto(ctx, "delete_group_file", base.Json{
		"group_id": d.GroupID,
		"file_id":  obj.GetID(),
		"busid":    defaultBusid,
	}, nil)
}

func (d *QQGroup) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	name := file.GetName()
	if strings.ContainsAny(name, `/\`) {
		return nil, errs.NewErr(errs.NotSupport, "文件名不能包含路径分隔符")
	}
	fileID, err := d.upload(ctx, name, d.normDirID(dstDir.GetID()), file, up)
	if err != nil {
		return nil, err
	}
	return &model.Object{
		ID:       fileID,
		Path:     stdpath.Join(dstDir.GetPath(), name),
		Name:     name,
		Size:     file.GetSize(),
		Modified: time.Now(),
	}, nil
}
func (d *QQGroup) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	info, err := d.getFileSystemInfo(ctx)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: info.TotalSpace,
			UsedSpace:  info.UsedSpace,
		},
	}, nil
}

func (d *QQGroup) getFileSystemInfo(ctx context.Context) (*FileSystemInfo, error) {
	var info FileSystemInfo
	err := d.callInto(ctx, "get_group_file_system_info", base.Json{"group_id": d.GroupID}, &info)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// list 列出 dirID 下的内容，dirPath 只用于拼出子对象的 Path
func (d *QQGroup) list(ctx context.Context, dirID, dirPath string) ([]model.Obj, error) {
	var resp ListResp
	var err error
	if d.isRootID(dirID) {
		err = d.callInto(ctx, "get_group_root_files", base.Json{"group_id": d.GroupID}, &resp)
	} else {
		err = d.callInto(ctx, "get_group_files_by_folder", base.Json{
			"group_id":  d.GroupID,
			"folder_id": dirID,
		}, &resp)
	}
	if err != nil {
		return nil, err
	}
	objs := make([]model.Obj, 0, len(resp.Files)+len(resp.Folders))
	for _, f := range resp.Files {
		objs = append(objs, &model.Object{
			ID:       f.FileID,
			Path:     stdpath.Join(dirPath, f.FileName),
			Name:     f.FileName,
			Size:     f.FileSize,
			Modified: timestamp(f.ModifyTime, f.UploadTime),
			Ctime:    timestamp(f.UploadTime),
		})
	}
	for _, f := range resp.Folders {
		objs = append(objs, &model.Object{
			ID:       f.FolderID,
			Path:     stdpath.Join(dirPath, f.FolderName),
			Name:     f.FolderName,
			Modified: timestamp(f.CreateTime),
			Ctime:    timestamp(f.CreateTime),
			IsFolder: true,
		})
	}
	return objs, nil
}

// dirIDByPath 把群文件内部的路径解析成 folder_id，根目录返回挂载点
func (d *QQGroup) dirIDByPath(ctx context.Context, dirPath string) (string, error) {
	if dirPath == "" || dirPath == "." || utils.PathEqual(dirPath, "/") {
		return d.normDirID(d.rootID()), nil
	}
	dirID := d.rootID()
	for _, name := range strings.Split(strings.Trim(dirPath, "/"), "/") {
		var obj model.Obj
		objs, err := d.list(ctx, dirID, "/")
		if err != nil {
			return "", err
		}
		for _, o := range objs {
			if o.GetName() == name && o.IsDir() {
				obj = o
				break
			}
		}
		if obj == nil {
			return "", errs.NewErr(errs.ObjectNotFound, "群文件里没有文件夹 %s", name)
		}
		dirID = obj.GetID()
	}
	return dirID, nil
}

func (d *QQGroup) rootID() string {
	return strings.TrimSpace(d.RootFolderID)
}

func (d *QQGroup) isRootID(id string) bool {
	id = strings.TrimSpace(id)
	return id == "" || id == "/"
}

// normDirID 归一化成 OneBot 认的目录标识，根目录用 "/"
func (d *QQGroup) normDirID(id string) string {
	if d.isRootID(id) {
		return "/"
	}
	return id
}

func timestamp(times ...int64) time.Time {
	for _, t := range times {
		if t > 0 {
			return time.Unix(t, 0)
		}
	}
	return time.Time{}
}

var _ driver.Driver = (*QQGroup)(nil)
var _ driver.GetRooter = (*QQGroup)(nil)
var _ driver.Getter = (*QQGroup)(nil)
var _ driver.MkdirResult = (*QQGroup)(nil)
var _ driver.MoveResult = (*QQGroup)(nil)
var _ driver.RenameResult = (*QQGroup)(nil)
var _ driver.Remove = (*QQGroup)(nil)
var _ driver.PutResult = (*QQGroup)(nil)
var _ driver.WithDetails = (*QQGroup)(nil)
