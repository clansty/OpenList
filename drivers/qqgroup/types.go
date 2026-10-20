package qqgroup

import "encoding/json"

// 群文件的 file_id / folder_id 都是 "/uuid" 形式，且在重命名、移动后保持不变

type OneBotFile struct {
	GroupID       int64  `json:"group_id"`
	FileID        string `json:"file_id"`
	FileName      string `json:"file_name"`
	Busid         int    `json:"busid"`
	FileSize      int64  `json:"file_size"`
	UploadTime    int64  `json:"upload_time"`
	DeadTime      int64  `json:"dead_time"`
	ModifyTime    int64  `json:"modify_time"`
	DownloadTimes int    `json:"download_times"`
	Uploader      int64  `json:"uploader"`
	UploaderName  string `json:"uploader_name"`
}

type OneBotFolder struct {
	GroupID        int64  `json:"group_id"`
	FolderID       string `json:"folder_id"`
	FolderName     string `json:"folder_name"`
	CreateTime     int64  `json:"create_time"`
	Creator        int64  `json:"creator"`
	CreatorName    string `json:"creator_name"`
	TotalFileCount int64  `json:"total_file_count"`
}

type ListResp struct {
	Files   []OneBotFile   `json:"files"`
	Folders []OneBotFolder `json:"folders"`
}

type FileSystemInfo struct {
	FileCount  int64 `json:"file_count"`
	LimitCount int64 `json:"limit_count"`
	UsedSpace  int64 `json:"used_space"`
	TotalSpace int64 `json:"total_space"`
}

type apiResp struct {
	Status  string          `json:"status"`
	Retcode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
	Wording string          `json:"wording"`
}
