package baidu

// AuthCodeOut is the response for device code request
type AuthCodeOut struct {
	DeviceCode      string `json:"device_code"`      //设备码，可用于生成单次凭证 Access Token。
	UserCode        string `json:"user_code"`        //用户码。 如果选择让用户输入 user code 方式，来引导用户授权，设备需要展示 user code 给用户。
	VerificationURL string `json:"verification_url"` //	用户输入 user code 进行授权的 url。
	QrcodeURL       string `json:"qrcode_url"`       //二维码url，用户用手机等智能终端扫描该二维码完成授权。
	ExpiresIn       int    `json:"expires_in"`       //device_code 的过期时间，单位：秒。到期后 device_code 不能换 Access Token。
	Interval        int    `json:"interval"`         //device_code 换 Access Token 轮询间隔时间，单位：秒。轮询次数限制小于 expire_in/interval。
}

// AccessTokenOut is the response for access token request
type AccessTokenOut struct {
	ExpiresIn     int    `json:"expires_in"`
	RefreshToken  string `json:"refresh_token"`
	AccessToken   string `json:"access_token"`
	SessionSecret string `json:"session_secret"`
	SessionKey    string `json:"session_key"`
	Scope         string `json:"scope"`
}

// FileEntity represents a single file or directory in Baidu
type FileEntity struct {
	FsID           uint64 `json:"fs_id"`           //文件在云端的唯一标识ID
	Path           string `json:"path"`            //文件的绝对路径
	ServerFilename string `json:"server_filename"` //文件名称
	Size           int64  `json:"size"`            //文件大小，单位B
	ServerMtime    int64  `json:"server_mtime"`    //文件在服务器修改时间
	ServerCtime    int64  `json:"server_ctime"`    //文件在服务器创建时间
	LocalMtime     int64  `json:"local_mtime"`     //文件在客户端修改时间
	LocalCtime     int64  `json:"local_ctime"`     //文件在客户端创建时间
	IsDir          uint   `json:"isdir"`           //是否为目录，0 文件、1 目录
	Md5            string `json:"md5"`             //云端哈希（非文件真实MD5），只有是文件类型时，该字段才存在
	DirEmpty       int    `json:"dir_empty"`       //该目录是否存在子目录，只有请求参数web=1且该条目为目录时，该字段才存在， 0为存在， 1为不存在
}

// FileListOut is the response for file list request
type FileListOut struct {
	ErrorOut
	List []FileEntity `json:"list"`
}

// ErrorOut is the common error response from Baidu
type ErrorOut struct {
	Errno     int    `json:"errno"`
	ErrMsg    string `json:"errmsg"`
	ErrorCode int    `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`
}

// MkdirOut is the response for mkdir request
type MkdirOut struct {
	ErrorOut
	Ctime    uint   `json:"ctime"`
	Mtime    uint   `json:"mtime"`
	FsID     uint64 `json:"fs_id"`
	IsDir    uint   `json:"is_dir"`
	Path     string `json:"path"`
	Status   uint   `json:"status"`
	Category uint   `json:"category"`
}

// QuotaOut is the response for quota request
type QuotaOut struct {
	Total  int64 `json:"total"`  //总空间大小，单位B
	Expire bool  `json:"expire"` //7天内是否有容量到期
	Used   int64 `json:"used"`   //已使用大小，单位B
	Free   int64 `json:"free"`   //剩余大小，单位B
}

// PreUploadOut is the response for pre-upload request
type PreUploadOut struct {
	ErrorOut
	Path       string `json:"path"`        //文件的绝对路径
	UploadID   string `json:"uploadid"`    //上传唯一ID标识此上传任务
	ReturnType int    `json:"return_type"` //返回类型，系统内部状态字段
	BlockList  []int  `json:"block_list"`  //需要上传的分片序号列表，索引从0开始
}

// SliceUploadOut is the response for slice upload request
type SliceUploadOut struct {
	ErrorOut
	Md5 string `json:"md5"` //
}

// RapidUploadOut is the response for rapid upload request
type RapidUploadOut struct {
	ErrorOut
	Info FileEntity `json:"info"`
}

/**
list	json array	文件信息列表
names	json	如果查询共享目录，该字段为共享目录文件上传者的uk和账户名称
list[0] ["category"]	int	文件类型，含义如下：1 视频， 2 音乐，3 图片，4 文档，5 应用，6 其他，7 种子
list[0] ["dlink”]	string	文件下载地址，参考下载文档进行下载操作
list[0] ["file_name”]	string	文件名
list[0] ["isdir”]	int	是否是目录，为1表示目录，为0表示非目录
list[0] ["server_ctime”]	int	文件的服务器创建Unix时间戳，单位秒
list[0] ["server_mtime”]	int	文件的服务器修改Unix时间戳，单位秒
list[0] ["size”]	int	文件大小，单位字节
list[0] ["thumbs”]	json	缩略图地址
list[0] ["height”]	int	图片高度
list[0] ["width”]	int	图片宽度
list[0] ["date_taken”]	int	图片拍摄时间
*/

// FileInfoListOut is the response for file info request
type FileInfoListOut struct {
	ErrorOut
	List []DownloadURL `json:"list"`
}

// DownloadURL represents a download address with file metadata
type DownloadURL struct {
	FileEntity
	Dlink string `json:"dlink"`
}

// SimpleUploadOut is the response for simple upload (single part)
type SimpleUploadOut struct {
	ErrorOut
	FileEntity
}
