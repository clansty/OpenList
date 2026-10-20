package qqgroup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
)

const testGroupID = 123456

// fakeOneBot 是个只够用的 OneBot 11 群文件服务端，folder 只有一层，和 LLBot 的行为一致
type fakeOneBot struct {
	mu      sync.Mutex
	folders map[string]string // folder_id -> name
	files   map[string]string // file_id -> name
	content map[string][]byte // file_id -> 内容
	parent  map[string]string // file_id -> folder_id（空字符串表示根目录）
	seq     int
	lastErr string
}

func newFakeOneBot() *fakeOneBot {
	return &fakeOneBot{
		folders: map[string]string{},
		files:   map[string]string{},
		content: map[string][]byte{},
		parent:  map[string]string{},
	}
}

func (f *fakeOneBot) newID(prefix string) string {
	f.seq++
	return fmt.Sprintf("/%s%d", prefix, f.seq)
}

func (f *fakeOneBot) addFolder(name string) string {
	id := f.newID("folder")
	f.folders[id] = name
	return id
}

func (f *fakeOneBot) addFile(name, folderID string, content []byte) string {
	id := f.newID("file")
	f.files[id] = name
	f.content[id] = content
	f.parent[id] = folderID
	return id
}

func writeData(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"retcode": 0,
		"data":    data,
		"message": "",
		"wording": "",
	})
}

func writeFailed(w http.ResponseWriter, msg string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "failed",
		"retcode": 200,
		"data":    nil,
		"message": msg,
		"wording": msg,
	})
}

func (f *fakeOneBot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	action := strings.TrimPrefix(r.URL.Path, "/")
	raw, _ := io.ReadAll(r.Body)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)

	if gid, ok := payload["group_id"].(float64); ok && int64(gid) != testGroupID {
		f.lastErr = "group_id 不对"
		writeFailed(w, "group_id 不对")
		return
	}

	switch action {
	case "get_group_file_system_info":
		var used int64
		for _, c := range f.content {
			used += int64(len(c))
		}
		writeData(w, map[string]any{
			"file_count":  len(f.files),
			"limit_count": 10000,
			"used_space":  used,
			"total_space": 1 << 30,
		})
	case "get_group_root_files":
		writeData(w, f.list(""))
	case "get_group_files_by_folder":
		folderID, _ := payload["folder_id"].(string)
		if folderID == "/" {
			folderID = ""
		}
		writeData(w, f.list(folderID))
	case "get_group_file_url":
		id, _ := payload["file_id"].(string)
		if _, ok := f.files[id]; !ok {
			f.lastErr = "file_id 不存在"
			writeFailed(w, "file_id 不存在")
			return
		}
		writeData(w, map[string]any{"url": "https://example.com/dl" + id})
	case "create_group_file_folder":
		name, _ := payload["name"].(string)
		writeData(w, map[string]any{"folder_id": f.addFolder(name)})
	case "rename_group_file_folder":
		id, _ := payload["folder_id"].(string)
		if _, ok := f.folders[id]; !ok {
			f.lastErr = "folder_id 不存在"
			writeFailed(w, "folder_id 不存在")
			return
		}
		f.folders[id], _ = payload["new_folder_name"].(string)
		writeData(w, nil)
	case "rename_group_file":
		id, _ := payload["file_id"].(string)
		parent, _ := payload["current_parent_directory"].(string)
		if parent == "/" {
			parent = ""
		}
		if f.parent[id] != parent {
			f.lastErr = fmt.Sprintf("current_parent_directory 不对: %q", parent)
			writeFailed(w, "请稍候重试")
			return
		}
		f.files[id], _ = payload["new_name"].(string)
		writeData(w, nil)
	case "move_group_file":
		id, _ := payload["file_id"].(string)
		parent, _ := payload["parent_directory"].(string)
		target, _ := payload["target_directory"].(string)
		if parent == "/" {
			parent = ""
		}
		if target == "/" {
			target = ""
		}
		if f.parent[id] != parent {
			f.lastErr = fmt.Sprintf("parent_directory 不对: %q", parent)
			writeFailed(w, "请稍候重试")
			return
		}
		if _, ok := f.folders[target]; !ok && target != "" {
			f.lastErr = "target_directory 不存在"
			writeFailed(w, "target_directory 不存在")
			return
		}
		f.parent[id] = target
		writeData(w, nil)
	case "delete_group_file":
		id, _ := payload["file_id"].(string)
		delete(f.files, id)
		delete(f.content, id)
		delete(f.parent, id)
		writeData(w, nil)
	case "delete_group_folder":
		id, _ := payload["folder_id"].(string)
		if _, ok := f.folders[id]; !ok {
			f.lastErr = "folder_id 不存在"
			writeFailed(w, "folder_id 不存在")
			return
		}
		delete(f.folders, id)
		for fileID, parent := range f.parent {
			if parent == id {
				delete(f.files, fileID)
				delete(f.content, fileID)
				delete(f.parent, fileID)
			}
		}
		writeData(w, nil)
	case "upload_group_file":
		var p struct {
			GroupID int64  `json:"group_id"`
			File    string `json:"file"`
			Name    string `json:"name"`
			Folder  string `json:"folder"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			f.lastErr = "上传 body 不是合法 JSON: " + err.Error()
			writeFailed(w, f.lastErr)
			return
		}
		if p.GroupID != testGroupID {
			f.lastErr = "上传的 group_id 不对"
			writeFailed(w, f.lastErr)
			return
		}
		content, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(p.File, "base64://"))
		if !strings.HasPrefix(p.File, "base64://") || err != nil {
			f.lastErr = "上传的 file 不是 base64://"
			writeFailed(w, f.lastErr)
			return
		}
		if p.Folder == "/" {
			p.Folder = ""
		}
		writeData(w, map[string]any{"file_id": f.addFile(p.Name, p.Folder, content)})
	default:
		f.lastErr = "未实现: " + action
		writeFailed(w, f.lastErr)
	}
}

func (f *fakeOneBot) list(folderID string) map[string]any {
	files := make([]OneBotFile, 0, len(f.files))
	for id, name := range f.files {
		if f.parent[id] != folderID {
			continue
		}
		files = append(files, OneBotFile{
			GroupID:    testGroupID,
			FileID:     id,
			FileName:   name,
			Busid:      defaultBusid,
			FileSize:   int64(len(f.content[id])),
			UploadTime: 1700000000,
			ModifyTime: 1700000001,
		})
	}
	folders := make([]OneBotFolder, 0, len(f.folders))
	// 真实实现（LLBot）不会在子目录里返回二级目录
	if folderID == "" {
		for id, name := range f.folders {
			folders = append(folders, OneBotFolder{
				GroupID:    testGroupID,
				FolderID:   id,
				FolderName: name,
				CreateTime: 1700000000,
			})
		}
	}
	return map[string]any{"files": files, "folders": folders}
}

func newTestDriver(t *testing.T) (*QQGroup, *fakeOneBot, func()) {
	t.Helper()
	prevConf := conf.Conf
	conf.Conf = &conf.Config{}
	t.Cleanup(func() { conf.Conf = prevConf })
	fake := newFakeOneBot()
	srv := httptest.NewServer(fake)
	d := &QQGroup{Addition: Addition{GroupID: testGroupID, APIURL: srv.URL}}
	if err := d.Init(context.Background()); err != nil {
		srv.Close()
		t.Fatalf("Init 失败: %v", err)
	}
	return d, fake, srv.Close
}

func findObj(t *testing.T, objs []model.Obj, name string) model.Obj {
	t.Helper()
	for _, obj := range objs {
		if obj.GetName() == name {
			return obj
		}
	}
	t.Fatalf("没找到 %s", name)
	return nil
}

func TestListRootAndFolder(t *testing.T) {
	d, fake, cleanup := newTestDriver(t)
	defer cleanup()
	folderID := fake.addFolder("子目录")
	fake.addFile("root.txt", "", []byte("root"))
	fake.addFile("inner.txt", folderID, []byte("inner"))

	root, err := d.List(context.Background(), &model.Object{Path: "/", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("List root 失败: %v", err)
	}
	folder := findObj(t, root, "子目录")
	if folder.GetID() != folderID || !folder.IsDir() || folder.GetPath() != "/子目录" {
		t.Fatalf("目录对象不对: %+v", folder)
	}
	file := findObj(t, root, "root.txt")
	if file.GetSize() != 4 || file.GetPath() != "/root.txt" || file.ModTime().Unix() != 1700000001 {
		t.Fatalf("文件对象不对: %+v", file)
	}

	inner, err := d.List(context.Background(), folder, model.ListArgs{})
	if err != nil {
		t.Fatalf("List 子目录失败: %v", err)
	}
	if len(inner) != 1 || findObj(t, inner, "inner.txt").GetPath() != "/子目录/inner.txt" {
		t.Fatalf("子目录内容不对: %+v", inner)
	}
}

func TestGetByPath(t *testing.T) {
	d, fake, cleanup := newTestDriver(t)
	defer cleanup()
	folderID := fake.addFolder("子目录")
	fake.addFile("inner.txt", folderID, []byte("inner"))

	obj, err := d.Get(context.Background(), "/子目录/inner.txt")
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if obj.GetName() != "inner.txt" || obj.GetPath() != "/子目录/inner.txt" {
		t.Fatalf("对象不对: %+v", obj)
	}
	if _, err = d.Get(context.Background(), "/不存在"); !errs.IsObjectNotFound(err) {
		t.Fatalf("期望 ObjectNotFound，实际 %v", err)
	}
}

func TestLink(t *testing.T) {
	d, fake, cleanup := newTestDriver(t)
	defer cleanup()
	fileID := fake.addFile("a.txt", "", []byte("a"))

	link, err := d.Link(context.Background(), &model.Object{ID: fileID}, model.LinkArgs{})
	if err != nil {
		t.Fatalf("Link 失败: %v", err)
	}
	if link.URL != "https://example.com/dl"+fileID {
		t.Fatalf("直链不对: %s", link.URL)
	}
}

func TestMakeDir(t *testing.T) {
	d, fake, cleanup := newTestDriver(t)
	defer cleanup()

	obj, err := d.MakeDir(context.Background(), &model.Object{Path: "/", IsFolder: true}, "新目录")
	if err != nil {
		t.Fatalf("MakeDir 失败: %v", err)
	}
	if fake.folders[obj.GetID()] != "新目录" || obj.GetPath() != "/新目录" || !obj.IsDir() {
		t.Fatalf("新目录不对: %+v", obj)
	}

	// 只在根目录能建，子目录里应当报 NotSupport
	_, err = d.MakeDir(context.Background(), obj, "孙目录")
	if !errs.IsNotSupportError(err) {
		t.Fatalf("期望 NotSupport，实际 %v", err)
	}
}

func TestRenameAndMove(t *testing.T) {
	d, fake, cleanup := newTestDriver(t)
	defer cleanup()
	folderID := fake.addFolder("子目录")
	fileID := fake.addFile("a.txt", "", []byte("a"))

	objs, err := d.List(context.Background(), &model.Object{Path: "/", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	file := findObj(t, objs, "a.txt")

	renamed, err := d.Rename(context.Background(), file, "b.txt")
	if err != nil {
		t.Fatalf("Rename 失败: %v", err)
	}
	if fake.files[fileID] != "b.txt" || renamed.GetPath() != "/b.txt" {
		t.Fatalf("重命名结果不对: %+v", renamed)
	}

	folder := findObj(t, objs, "子目录")
	moved, err := d.Move(context.Background(), renamed, folder)
	if err != nil {
		t.Fatalf("Move 失败: %v", err)
	}
	if fake.parent[fileID] != folderID || moved.GetPath() != "/子目录/b.txt" {
		t.Fatalf("移动结果不对: %+v", moved)
	}

	folderRenamed, err := d.Rename(context.Background(), folder, "改名目录")
	if err != nil {
		t.Fatalf("Rename 目录失败: %v", err)
	}
	if fake.folders[folderID] != "改名目录" || folderRenamed.GetPath() != "/改名目录" {
		t.Fatalf("目录重命名结果不对: %+v", folderRenamed)
	}
}

func TestPut(t *testing.T) {
	d, fake, cleanup := newTestDriver(t)
	defer cleanup()
	folderID := fake.addFolder("子目录")

	content := bytes.Repeat([]byte("openlist"), 1024)
	file := &stream.FileStream{
		Ctx:    context.Background(),
		Obj:    &model.Object{Name: "上传.bin", Size: int64(len(content))},
		Reader: bytes.NewReader(content),
	}
	var progress []float64
	up := func(p float64) { progress = append(progress, p) }

	obj, err := d.Put(context.Background(), &model.Object{ID: folderID, Path: "/子目录", IsFolder: true}, file, up)
	if err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	stored := fake.content[obj.GetID()]
	if !bytes.Equal(stored, content) {
		t.Fatalf("上传内容不一致，长度 %d", len(stored))
	}
	if fake.parent[obj.GetID()] != folderID || obj.GetName() != "上传.bin" || obj.GetPath() != "/子目录/上传.bin" {
		t.Fatalf("上传对象不对: %+v", obj)
	}
	if len(progress) == 0 || progress[len(progress)-1] != 100 {
		t.Fatalf("进度上报不对: %v", progress)
	}
	if fake.lastErr != "" {
		t.Fatalf("服务端报错: %s", fake.lastErr)
	}
}

func TestRemoveAndDetails(t *testing.T) {
	d, fake, cleanup := newTestDriver(t)
	defer cleanup()
	folderID := fake.addFolder("子目录")
	fileID := fake.addFile("a.txt", "", []byte("12345"))

	details, err := d.GetDetails(context.Background())
	if err != nil {
		t.Fatalf("GetDetails 失败: %v", err)
	}
	if details.TotalSpace != 1<<30 || details.UsedSpace != 5 {
		t.Fatalf("空间信息不对: %+v", details)
	}

	if err = d.Remove(context.Background(), &model.Object{ID: fileID, Name: "a.txt"}); err != nil {
		t.Fatalf("删除文件失败: %v", err)
	}
	if _, ok := fake.files[fileID]; ok {
		t.Fatalf("文件没删掉")
	}
	if err = d.Remove(context.Background(), &model.Object{ID: folderID, Name: "子目录", IsFolder: true}); err != nil {
		t.Fatalf("删除目录失败: %v", err)
	}
	if _, ok := fake.folders[folderID]; ok {
		t.Fatalf("目录没删掉")
	}
}

func TestInitWithBadGroup(t *testing.T) {
	prevConf := conf.Conf
	conf.Conf = &conf.Config{}
	t.Cleanup(func() { conf.Conf = prevConf })
	fake := newFakeOneBot()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	d := &QQGroup{Addition: Addition{GroupID: 1, APIURL: srv.URL}}
	if err := d.Init(context.Background()); err == nil {
		t.Fatalf("群号不对时 Init 应当失败")
	}
	var _ driver.Driver = d
}

func TestDriverInfo(t *testing.T) {
	info, ok := op.GetDriverInfoMap()[config.Name]
	if !ok {
		t.Fatalf("驱动 %s 没有注册", config.Name)
	}
	names := map[string]string{}
	for _, item := range info.Additional {
		names[item.Name] = item.Type
	}
	for name, wantType := range map[string]string{
		"group_id":       "number",
		"api_url":        "string",
		"token":          "string",
		"root_folder_id": "string",
	} {
		if names[name] != wantType {
			t.Fatalf("配置项 %s 类型是 %q，期望 %q（全部配置项: %v）", name, names[name], wantType, names)
		}
	}
}

func TestMountSubFolder(t *testing.T) {
	prevConf := conf.Conf
	conf.Conf = &conf.Config{}
	t.Cleanup(func() { conf.Conf = prevConf })

	fake := newFakeOneBot()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	mountID := fake.addFolder("挂载点")
	fake.addFile("in-mount.txt", mountID, []byte("abc"))

	d := &QQGroup{Addition: Addition{GroupID: testGroupID, APIURL: srv.URL, RootID: driver.RootID{RootFolderID: mountID}}}
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	root, err := d.GetRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	objs, err := d.List(context.Background(), root, model.ListArgs{})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	file := findObj(t, objs, "in-mount.txt")
	if file.GetPath() != "/in-mount.txt" {
		t.Fatalf("挂载点下的路径不对: %s", file.GetPath())
	}

	// 挂载点就是群文件里的子目录，上传和移动都不能落到群根目录
	obj, err := d.Put(context.Background(), root, &stream.FileStream{
		Ctx:    context.Background(),
		Obj:    &model.Object{Name: "new.txt", Size: 3},
		Reader: strings.NewReader("new"),
	}, func(float64) {})
	if err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	if fake.parent[obj.GetID()] != mountID {
		t.Fatalf("文件没上传到挂载点: %q", fake.parent[obj.GetID()])
	}
	if _, err = d.Rename(context.Background(), obj, "new2.txt"); err != nil {
		t.Fatalf("Rename 失败: %v", err)
	}
	sub, err := d.MakeDir(context.Background(), root, "挂载点子目录")
	if !errs.IsNotSupportError(err) {
		t.Fatalf("挂载点不是群根目录，MakeDir 应当 NotSupport，实际 %v", err)
	}
	_ = sub
}
