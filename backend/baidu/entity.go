package baidu

// AuthCodeOut is the response for device code request
type AuthCodeOut struct {
	DeviceCode      string `json:"device_code"`      // Device code for generating access token
	UserCode        string `json:"user_code"`        // User code for device authorization
	VerificationURL string `json:"verification_url"` // URL where user enters the code
	QrcodeURL       string `json:"qrcode_url"`       // QR code URL for mobile authorization
	ExpiresIn       int    `json:"expires_in"`       // Expiration time in seconds
	Interval        int    `json:"interval"`         // Polling interval in seconds
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
	FsID           uint64 `json:"fs_id"`           // File ID in cloud
	Path           string `json:"path"`            // Absolute path
	ServerFilename string `json:"server_filename"` // Filename
	Size           int64  `json:"size"`            // File size in bytes
	ServerMtime    int64  `json:"server_mtime"`    // Server modification time (Unix timestamp)
	ServerCtime    int64  `json:"server_ctime"`    // Server creation time (Unix timestamp)
	LocalMtime     int64  `json:"local_mtime"`     // Local modification time (Unix timestamp)
	LocalCtime     int64  `json:"local_ctime"`     // Local creation time (Unix timestamp)
	IsDir          uint   `json:"isdir"`           // 0 = file, 1 = directory
	Md5            string `json:"md5"`             // Cloud hash (not real MD5)
	DirEmpty       int    `json:"dir_empty"`       // 0 = has subdirectories, 1 = empty
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
	Total  int64 `json:"total"`  // Total space in bytes
	Expire bool  `json:"expire"` // Whether quota expires in 7 days
	Used   int64 `json:"used"`   // Used space in bytes
	Free   int64 `json:"free"`   // Free space in bytes
}

// PreUploadOut is the response for pre-upload request
type PreUploadOut struct {
	ErrorOut
	Path       string `json:"path"`        // File absolute path
	UploadID   string `json:"uploadid"`    // Upload task ID
	ReturnType int    `json:"return_type"` // Return type (internal status)
	BlockList  []int  `json:"block_list"`  // List of block indices to upload (0-indexed)
}

// SliceUploadOut is the response for slice upload request
type SliceUploadOut struct {
	ErrorOut
	Md5 string `json:"md5"` // MD5 of uploaded slice
}

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
