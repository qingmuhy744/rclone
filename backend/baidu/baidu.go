// Package baidu provides a backend for BaiduYun Drive
package baidu

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rclone/rclone/fs/config/configstruct"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/rest"
)

const (
	openAPIURL = "https://openapi.baidu.com"
	rootURL    = "https://pan.baidu.com"
	uploadURL  = "https://c.pcs.baidu.com"
	rootID     = "/"

	// API endpoints
	uriOauthCode  = "/oauth/2.0/device/code"
	uriOauthToken = "/oauth/2.0/token"
	uriFile       = "/rest/2.0/xpan/file"
	uriSuperFile  = "/rest/2.0/pcs/superfile2"
	uriPCSFile    = "/rest/2.0/pcs/file"
	uriMultimedia = "/rest/2.0/xpan/multimedia"
	uriQuota      = "/api/quota"

	// Upload configuration
	chunkSize = 4 * 1024 * 1024 // Chunk size fixed at 4MB (official standard for all user types)
)

// Options defines the configuration for this backend
type Options struct {
	AppKey       string `config:"app_key"`
	SecretKey    string `config:"secret_key"`
	AccessToken  string `config:"access_token"`
	RefreshToken string `config:"refresh_token"`
	ExpiresIn    int    `config:"expires_in"`
	ExpiresAt    string `config:"expires_at"`
}

// Register with Fs
func init() {
	fs.Register(&fs.RegInfo{
		Name:        "baidu",
		Description: "BaiduYun Drive",
		NewFs:       NewFs,
		Config:      Config,
		Options: []fs.Option{{
			Name:     "app_key",
			Help:     "Please enter AppKey",
			Required: true,
		}, {
			Name:     "secret_key",
			Help:     "Please enter SecretKey",
			Required: true,
		}},
	})
}

// Config callback
func Config(ctx context.Context, name string, m configmap.Mapper, c fs.ConfigIn) (*fs.ConfigOut, error) {
	appKey, ok := m.Get("app_key")
	if !ok {
		return nil, errors.New("AppKey not found")
	}

	secretKey, ok := m.Get("secret_key")
	if !ok {
		return nil, errors.New("SecretKey not found")
	}

	auth, err := authCode(ctx, appKey)
	if err != nil {
		return nil, err
	}

	fmt.Printf("请在浏览器中打开链接：%s ,并在打开的页面中输入：%s 获取授权。\n", auth.VerificationURL, auth.UserCode)

	for {
		token, err := getAccessToken(ctx, auth.DeviceCode, appKey, secretKey)
		if err == nil && token.AccessToken != "" && token.RefreshToken != "" {
			m.Set("access_token", token.AccessToken)
			m.Set("refresh_token", token.RefreshToken)
			m.Set("expires_in", strconv.Itoa(token.ExpiresIn))
			m.Set("expires_at", time.Now().Add(time.Duration(token.ExpiresIn-600)*time.Second).Format("2006-01-02 15:04:05"))
			break
		}
		time.Sleep(time.Second * time.Duration(auth.Interval))
	}
	return nil, nil
}

func getAccessToken(ctx context.Context, deviceCode, appKey, secretKey string) (*AccessTokenOut, error) {
	c := rest.NewClient(fshttp.NewClient(ctx))
	opts := &rest.Opts{
		Method:  "GET",
		RootURL: openAPIURL,
		Path:    uriOauthToken,
		Parameters: map[string][]string{
			"grant_type":    {"device_token"},
			"code":          {deviceCode},
			"client_id":     {appKey},
			"client_secret": {secretKey},
		},
	}
	c.SetHeader("User-Agent", "pan.baidu.com")
	resp := &AccessTokenOut{}
	_, err := c.CallJSON(ctx, opts, nil, resp)
	return resp, err
}

// authCode requests device authorization code from Baidu OAuth2 API
func authCode(ctx context.Context, appKey string) (*AuthCodeOut, error) {
	c := rest.NewClient(fshttp.NewClient(ctx))
	opts := &rest.Opts{
		Method:  "GET",
		RootURL: openAPIURL,
		Path:    uriOauthCode,
		Parameters: map[string][]string{
			"response_type": {"device_code"},
			"client_id":     {appKey},
			"scope":         {"basic,netdisk"},
		},
	}
	c.SetHeader("User-Agent", "pan.baidu.com")
	resp := &AuthCodeOut{}
	_, err := c.CallJSON(ctx, opts, nil, resp)
	return resp, err
}

// NewFs constructs an Fs from the path, bucket:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	ci := fs.GetConfig(ctx)

	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}

	f := &Fs{
		name:        name,
		ci:          ci,
		srv:         rest.NewClient(fshttp.NewClient(ctx)),
		downloadSrv: rest.NewClient(fshttp.NewClient(ctx)),
		root:        "/" + strings.Trim(root, "/"),
		ctx:         ctx,
		opt:         opt,
		m:           m,
	}

	f.features = (&fs.Features{
		CanHaveEmptyDirectories: true,
	}).Fill(ctx, f)
	go f.reWriteConfig()
	return f, err
}

func (f *Fs) reWriteConfig() {
	for {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", f.opt.ExpiresAt, time.Local)
		if err != nil {
			t = time.Now()
		}
		sub := time.Until(t)
		if sub < 0 {
			sub = 5 * time.Minute
		}
		trigger := time.After(sub)
		<-trigger
		err = f.refreshToken()
		if err != nil {
			fs.Errorf(f, "Failed to refresh token: %v", err)
		}
	}
}

// Fs represents a remote Baidu server
type Fs struct {
	name        string
	ci          *fs.ConfigInfo
	srv         *rest.Client
	downloadSrv *rest.Client
	features    *fs.Features
	root        string
	ctx         context.Context
	opt         *Options
	m           configmap.Mapper
}

func (f *Fs) call(ctx context.Context, opts *rest.Opts, response interface{}) error {
	// Set access token for API requests
	opts.Parameters.Set("access_token", f.opt.AccessToken)
	if opts.ExtraHeaders == nil {
		opts.ExtraHeaders = make(map[string]string)
	}
	// Impersonate Baidu Cloud Manager client for better compatibility
	opts.ExtraHeaders["User-Agent"] = "netdisk;P2SP;8.3.1.2;PC;PC-Windows;10.0.19042;WindowsBaiduYunGuanJia"
	resp, err := f.srv.Call(ctx, opts)
	if err != nil {
		return err
	}

	respError := ErrorOut{}
	b, _ := io.ReadAll(resp.Body)
	err = json.Unmarshal(b, &respError)
	if err != nil {
		return fmt.Errorf("failed to unmarshal error response: %w", err)
	}
	// Check both errno and error_code for API errors
	if respError.Errno != 0 || respError.ErrorCode != 0 {
		errno := respError.Errno
		errmsg := respError.ErrMsg
		if respError.ErrorCode != 0 {
			errno = respError.ErrorCode
			errmsg = respError.ErrorMsg
		}
		if errno == 111 || errno == -6 || errno == 110 {
			err = f.refreshToken()
			if err != nil {
				return err
			}
			return f.call(ctx, opts, response)
		}
		return fmt.Errorf("errno: %d,errmsg: %s", errno, errmsg)
	}
	err = json.Unmarshal(b, response)
	if err != nil {
		return fmt.Errorf("failed to unmarshal response: %w", err)
	}
	return nil
}

func (f *Fs) fullPath(remote string) string {
	return "/" + strings.TrimLeft(path.Join(f.root, remote), "/")
}

func (f *Fs) download(ctx context.Context, opts *rest.Opts) (resp *http.Response, err error) {
	// Set access token for API requests
	//opts.Parameters.Set("access_token", f.opt.AccessToken)
	f.downloadSrv.SetHeader("Host", "d.pcs.baidu.com")
	return f.downloadSrv.Call(ctx, opts)
}

func (f *Fs) refreshToken() error {
	opts := &rest.Opts{
		Method:  "GET",
		RootURL: openAPIURL,
		Path:    uriOauthToken,
		Parameters: map[string][]string{
			"grant_type":    {"refresh_token"},
			"refresh_token": {f.opt.RefreshToken},
			"client_id":     {f.opt.AppKey},
			"client_secret": {f.opt.SecretKey},
		},
	}
	f.srv.SetHeader("User-Agent", "pan.baidu.com")
	token := &AccessTokenOut{}
	_, err := f.srv.CallJSON(f.ctx, opts, nil, token)
	if err != nil {
		return err
	}

	if token.AccessToken != "" && token.RefreshToken != "" {
		f.m.Set("access_token", token.AccessToken)
		f.m.Set("refresh_token", token.RefreshToken)
		f.m.Set("expires_in", strconv.Itoa(token.ExpiresIn))
		f.m.Set("expires_at", time.Now().Add(time.Duration(token.ExpiresIn-600)*time.Second).Format("2006-01-02 15:04:05"))
	}
	return configstruct.Set(f.m, f.opt)
}

// Name returns the name of this backend
func (f *Fs) Name() string {
	return f.name
}

// Root of the remote (as passed into NewFs)
func (f *Fs) Root() string {
	return f.root
}

// String converts this Fs to a string
func (f *Fs) String() string {
	return "BaiduYun Drive"
}

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features {
	return f.features
}

// Precision of the ModTimes in this Fs
func (f *Fs) Precision() time.Duration {
	return time.Second
}

// Hashes returns the supported hash types of the filesystem
func (f *Fs) Hashes() hash.Set {
	return hash.NewHashSet(hash.None)
}

// List the objects and directories in dir into entries.  The
// entries can be returned in any order but should be for a
// complete directory.
//
// dir should be "" to list the root, and should not have
// trailing slashes.
//
// This should return ErrDirNotFound if the directory isn't
// found.
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	list, err := f.listDirAllFile(ctx, f.fullPath(dir))
	if err != nil {
		return nil, err
	}

	for _, info := range list {
		remote := strings.TrimLeft(strings.TrimPrefix(info.Path, f.root), "/")
		if remote == "" {
			continue
		}

		var item fs.DirEntry
		if info.IsDir == 1 {
			item = fs.NewDir(remote, time.Unix(info.ServerMtime, 0)).SetID(strconv.FormatUint(info.FsID, 10))
		} else {
			item = &Object{
				fs:      f,
				remote:  remote,
				path:    info.Path,
				size:    info.Size,
				id:      strconv.FormatUint(info.FsID, 10),
				modTime: time.Unix(info.ServerMtime, 0),
			}
		}
		entries = append(entries, item)
	}
	return
}

func (f *Fs) listDirAllFile(ctx context.Context, dir string) ([]FileEntity, error) {
	start := 0
	var all []FileEntity
	for {
		list, err := f.listDirFile(ctx, dir, start, 1000)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			break
		}
		start += 1000
		all = append(all, list...)
	}
	return all, nil
}

func (f *Fs) listDirFile(ctx context.Context, dir string, start, limit int) ([]FileEntity, error) {
	opts := &rest.Opts{
		Method:  "GET",
		RootURL: rootURL,
		Path:    uriFile,
		Parameters: map[string][]string{
			"method":       {"list"},
			"access_token": {f.opt.AccessToken},
			"dir":          {dir},
			"start":        {strconv.Itoa(start)},
			"limit":        {strconv.Itoa(limit)},
			"web":          {"1"},
			"folder":       {"0"},
			"showempty":    {"1"},
			"openapi":      {"xpansdk"},
		},
	}
	resp := &FileListOut{}
	err := f.call(ctx, opts, resp)
	if err != nil {
		if strings.Contains(err.Error(), "errno: -9") {
			return nil, nil // Path doesn't exist or is not a directory, return empty list
		}
		return nil, err
	}
	return resp.List, nil
}

// NewObject finds the Object at remote.  If it can't be found
// it returns the error ErrorObjectNotFound.
//
// If remote points to a directory then it should return
// ErrorIsDir if possible without doing any extra work,
// otherwise ErrorObjectNotFound.
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	return f.newObjectWithInfo(ctx, remote, nil)
}

func (f *Fs) newObjectWithInfo(ctx context.Context, remote string, info *FileEntity) (fs.Object, error) {
	o := &Object{
		fs:     f,
		remote: remote,
	}
	var err error
	if info != nil {
		err = o.setMetaData(info)
	} else {
		err = o.readMetaData(ctx)
	}
	return o, err
}

// Copy src to this remote using server-side copy operations.
//
// # This is stored with the remote path given
//
// # It returns the destination Object and a possible error
//
// Will only be called if src.Fs().Name() == f.Name()
//
// If it isn't possible then return fs.ErrorCantCopy
func (f *Fs) Copy(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		return nil, fs.ErrorCantCopy
	}

	if srcObj.path == remote {
		return nil, fs.ErrorCantCopy
	}
	p, name := path.Split(remote)
	fileList := fmt.Sprintf(`[{"path":"%s","dest":"%s","newname":"%s","ondup":"newcopy"}]`, srcObj.path, f.fullPath(p), name)
	err := f.fileManager(ctx, "copy", fileList)
	if err != nil {
		return nil, err
	}
	return f.newObject(remote, srcObj.size), nil
}

func (f *Fs) newObject(path string, size int64) *Object {
	return &Object{fs: f, remote: path, path: path, size: size, modTime: time.Now()}
}

// fileOperation is a unified method for file operations (copy/move)
func (f *Fs) fileManager(ctx context.Context, opera, fileList string) error {
	opts := &rest.Opts{
		Method:  "POST",
		RootURL: rootURL,
		Path:    uriFile,
		Parameters: map[string][]string{
			"method": {"filemanager"},
			"opera":  {opera},
		},
		Body: bytes.NewBuffer([]byte("async=1&ondup=newcopy&filelist=" + fileList)),
	}
	resp := &ErrorOut{}
	return f.call(ctx, opts, resp)
}

// Move src to this remote using server-side move operations.
//
// # This is stored with the remote path given
//
// # It returns the destination Object and a possible error
//
// Will only be called if src.Fs().Name() == f.Name()
//
// If it isn't possible then return fs.ErrorCantMove
func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		return nil, fs.ErrorCantMove
	}
	if srcObj.path == remote {
		return nil, fs.ErrorCantMove
	}
	p, name := path.Split(remote)
	fileList := fmt.Sprintf(`[{"path":"%s","dest":"%s","newname":"%s","ondup":"newcopy"}]`, srcObj.path, f.fullPath(p), name)
	err := f.fileManager(ctx, "move", fileList)
	if err != nil {
		return nil, err
	}
	srcObj.path = remote
	srcObj.remote = remote
	return srcObj, nil
}

// Put in to the remote path with the modTime given of the given size
//
// When called from outside an Fs by rclone, src.Size() will always be >= 0.
// But for unknown-sized objects (indicated by src.Size() == -1), Put should either
// return an error or upload it properly (rather than e.g. calling panic).
//
// May create the object even if it returns an error - if so
// will return the object and the error, otherwise will return
// nil and the error
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	return f.PutUnchecked(ctx, in, src, options...)
}

// PutUnchecked the object into the container
//
// # This will produce an error if the object already exists
//
// # Copy the reader in to the new object which is returned
//
// The new object may have been created if an error is returned
func (f *Fs) PutUnchecked(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	remote := src.Remote()
	size := src.Size()
	modTime := src.ModTime(ctx)

	o, err := f.createObject(ctx, remote, modTime, size)
	if err != nil {
		return nil, err
	}
	return o, o.Update(ctx, in, src, options...)

}

// Creates from the parameters passed in a half finished Object which
// must have setMetaData called on it
//
// # Returns the object, leaf, directoryID and error
//
// Used to create new objects
func (f *Fs) createObject(ctx context.Context, remote string, modTime time.Time, size int64) (o *Object, err error) {
	// Temporary Object under construction
	o = &Object{
		fs:      f,
		remote:  remote,
		modTime: modTime,
		size:    size,
	}
	return o, nil
}

// Mkdir makes the directory (container, bucket)
//
// Shouldn't return an error if it already exists
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	v := url.Values{}
	v.Set("path", f.fullPath(dir))
	v.Set("isdir", "1")
	v.Set("rtype", "0")

	opts := &rest.Opts{
		Method:  "POST",
		RootURL: rootURL,
		Path:    uriFile,
		Parameters: map[string][]string{
			"method": {"create"},
		},
		Body:        strings.NewReader(v.Encode()),
		ContentType: "application/x-www-form-urlencoded",
	}
	resp := MkdirOut{}
	err := f.call(ctx, opts, &resp)
	if err != nil && (strings.Contains(err.Error(), "errno: -8") || strings.Contains(err.Error(), "errno: -9")) {
		// -8: file already exists, -9: directory already exists
		// According to Mkdir spec, we shouldn't return an error if it already exists
		return nil
	}
	return err
}

// Rmdir removes the directory (container, bucket) if empty
//
// Return an error if it doesn't exist or isn't empty
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	fullDir := f.fullPath(dir)
	if fullDir == "/" {
		return errors.New("the root directory cannot be deleted")
	}
	list, err := f.listDirFile(ctx, fullDir, 0, 1)
	if err != nil {
		return err
	}
	if len(list) != 0 {
		return errors.New("directory is not be empty")
	}
	fileList := fmt.Sprintf(`[{"path":"%s"}]`, f.fullPath(dir))
	return f.fileManager(ctx, "delete", fileList)
}

// Purge all files in the directory specified
//
// Implement this if you have a way of deleting all the files
// quicker than just running Remove() on the result of List()
//
// Return an error if it doesn't exist
func (f *Fs) Purge(ctx context.Context, dir string) error {
	err := f.Rmdir(ctx, dir)
	if err != nil {
		return nil
	}
	return f.Mkdir(ctx, dir)
}

// About gets quota information
func (f *Fs) About(ctx context.Context) (usage *fs.Usage, err error) {
	opts := &rest.Opts{
		Method:  "GET",
		RootURL: rootURL,
		Path:    uriQuota,
		Parameters: map[string][]string{
			"openapi": {"xpansdk"},
		},
	}
	resp := QuotaOut{}
	if err = f.call(ctx, opts, &resp); err != nil {
		return nil, err
	}
	free := resp.Total - resp.Used
	usage = &fs.Usage{
		Free:  &free, // Baidu API often returns Free=0, manually calculated for rclone display
		Total: &resp.Total,
		Used:  &resp.Used,
	}
	return usage, nil
}

// Object describes a Baidu file
type Object struct {
	fs          *Fs // what this object is part of
	path        string
	remote      string    // The remote path
	size        int64     // size of the object
	modTime     time.Time // modification time of the object
	id          string    // ID of the object
	hasMetaData bool      //
}

// Fs returns the parent Fs
func (o *Object) Fs() fs.Info {
	return o.fs
}

// Return a string version
func (o *Object) String() string {
	if o == nil {
		return "<nil>"
	}
	return o.remote
}

// Remote returns the remote path
func (o *Object) Remote() string {
	return o.remote
}

// Hash returns the SHA-1 of an object returning a lowercase hex string
func (o *Object) Hash(ctx context.Context, t hash.Type) (string, error) {
	return "", nil
}

// Size returns the size of an object in bytes
func (o *Object) Size() int64 {
	return o.size
}

// ModTime returns the modification time of the object
//
// It attempts to read the objects mtime and if that isn't present the
// LastModified returned in the http headers
func (o *Object) ModTime(ctx context.Context) time.Time {
	return o.modTime
}

// SetModTime sets the modification time of the local fs object
func (o *Object) SetModTime(ctx context.Context, modTime time.Time) error {
	return nil
}

// Storable returns a boolean showing whether this object storable
func (o *Object) Storable() bool {
	return true
}

// Open an object for read
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (in io.ReadCloser, err error) {
	downloadURL, err := o.fileDownloadURL(ctx)
	if err != nil {
		return nil, err
	}
	return o.download(ctx, downloadURL, options...)
}

func (o *Object) download(ctx context.Context, downloadURL string, options ...fs.OpenOption) (in io.ReadCloser, err error) {
	fs.FixRangeOption(options, o.size)
	opts := rest.Opts{
		Method:     "GET",
		RootURL:    downloadURL + "&access_token=" + o.fs.opt.AccessToken,
		Parameters: map[string][]string{},
		//Options: options,
	}
	resp, err := o.fs.download(ctx, &opts)
	if err != nil {
		return nil, err
	}
	return resp.Body, err
}

func (o *Object) fileDownloadURL(ctx context.Context) (string, error) {
	opts := &rest.Opts{
		Method:  "POST",
		RootURL: rootURL,
		Path:    uriMultimedia,
		Parameters: map[string][]string{
			"method": {"filemetas"},
			"fsids":  {"[" + o.id + "]"},
			"dlink":  {"1"},
		},
	}
	resp := FileInfoListOut{}
	err := o.fs.call(ctx, opts, &resp)
	if err != nil {
		return "", err
	}
	if len(resp.List) == 0 {
		return "", errors.New("")
	}
	return resp.List[0].Dlink, nil
}

// Update the object with the contents of the io.Reader, modTime and size
//
// # If existing is set then it updates the object rather than creating a new one
//
// The new object may have been created if an error is returned
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (err error) {
	return o.upload(ctx, in, src.Size())
}

func (o *Object) upload(ctx context.Context, in io.Reader, size int64) error {
	remote := o.fs.fullPath(o.remote)

	// Log upload information and reader type detection
	_, isSeeker := in.(io.ReadSeeker)
	_, isAsyncReader := in.(interface{ Abandon() })
	fs.Debugf(o, "Starting upload: size=%d, isSeeker=%v, isAsyncReader=%v", size, isSeeker, isAsyncReader)

	// Note: Rapid upload is not supported due to rclone's AsyncReader wrapper.
	// The AsyncReader provides read-ahead buffering for performance but prevents
	// access to the original file path needed for hash pre-calculation.
	// This is a deliberate design trade-off in rclone that prioritizes throughput
	// over Seek capability.

	// 1. For files ≤4MB, use simple upload (single request)
	// According to Baidu docs, files >4MB must use chunked upload.
	// The PCS simple upload endpoint becomes unstable for large files.
	if size <= 4*1024*1024 && size >= 0 {
		return o.simpleUpload(ctx, in, size)
	}

	// 2. For files >4MB, use XPAN chunked upload (superfile2 API)
	// Note: Due to rclone's AsyncReader wrapper, Seek detection is unreliable.
	// We use disk spooling for large files to avoid OOM issues.

	var md5s []string
	var uploadSource io.ReaderAt
	// If the input is a Seeker (e.g., local file), try to use it directly
	if seeker, ok := in.(io.ReadSeeker); ok {
		// Double-check Seek capability to filter out AsyncReader's stub implementation
		if _, err := seeker.Seek(0, io.SeekCurrent); err == nil {
			fs.Debugf(o, "✓ Seek detection successful, using zero-copy dual-read mode (local file optimization)")
			uploadSource = seeker.(io.ReaderAt) // os.File implements ReaderAt

			// First pass: calculate MD5 for all chunks
			buf := make([]byte, chunkSize)
			for {
				n, err := io.ReadFull(seeker, buf)
				if n > 0 {
					h := md5.New()
					h.Write(buf[:n])
					md5s = append(md5s, hex.EncodeToString(h.Sum(nil)))
				}
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					break
				}
				if err != nil {
					return fmt.Errorf("failed to scan file MD5: %w", err)
				}
			}

			// Reset file pointer to beginning
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("failed to reset file pointer: %w", err)
			}

			goto DoUpload
		}
	}

	// For large non-seekable streams (>128MB), use temp file caching to avoid OOM
	if size > 128*1024*1024 {
		fs.Debugf(o, "→ Large file upload (%.2fMB), non-seekable, enabling disk cache mode", float64(size)/(1024*1024))
		tempFile, err := os.CreateTemp("", "rclone-baidu-upload-*")
		if err != nil {
			return fmt.Errorf("failed to create temp cache file: %w", err)
		}
		defer func() {
			_ = tempFile.Close()
			_ = os.Remove(tempFile.Name())
		}()

		uploadSource = tempFile // os.File implements ReaderAt

		buf := make([]byte, chunkSize)
		for {
			n, err := io.ReadFull(in, buf)
			if n > 0 {
				// Write to temp file
				if _, err := tempFile.Write(buf[:n]); err != nil {
					return fmt.Errorf("failed to write to temp cache file: %w", err)
				}

				// Calculate MD5
				h := md5.New()
				h.Write(buf[:n])
				md5s = append(md5s, hex.EncodeToString(h.Sum(nil)))
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			if err != nil {
				return err
			}
		}

		// Ensure data is flushed to disk
		if err := tempFile.Sync(); err != nil {
			return fmt.Errorf("failed to sync temp file: %w", err)
		}

		goto DoUpload
	}

	// 3. For smaller stream inputs (≤128MB), buffer in memory (faster)
	{
		fs.Debugf(o, "→ Small file stream upload (%.2fMB), non-seekable, using memory cache mode", float64(size)/(1024*1024))
		var chunks [][]byte
		buf := make([]byte, chunkSize)

		for {
			n, err := io.ReadFull(in, buf)
			if n > 0 {
				chunkCopy := make([]byte, n)
				copy(chunkCopy, buf[:n])
				chunks = append(chunks, chunkCopy)

				h := md5.New()
				h.Write(chunkCopy)
				md5s = append(md5s, hex.EncodeToString(h.Sum(nil)))
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			if err != nil {
				return err
			}
		}

		preResp, err := o.precreate(ctx, remote, size, md5s)
		if err != nil {
			return fmt.Errorf("precreate failed: %w", err)
		}
		uploadID := preResp.UploadID

		for i, chunkData := range chunks {
			_, err := o.sliceUpload(ctx, remote, uploadID, i, bytes.NewReader(chunkData), int64(len(chunkData)))
			if err != nil {
				return fmt.Errorf("slice %d upload failed: %w", i, err)
			}
		}

		md5ListBytes, _ := json.Marshal(md5s)
		file, err := o.complete(ctx, remote, uploadID, size, string(md5ListBytes))
		if err != nil {
			return fmt.Errorf("failed to merge file: %w", err)
		}
		o.id = strconv.FormatUint(file.FsID, 10)
		o.path = file.Path
		return nil
	}

DoUpload:
	preResp, err := o.precreate(ctx, remote, size, md5s)
	if err != nil {
		return fmt.Errorf("precreate failed: %w", err)
	}
	uploadID := preResp.UploadID

	for i := 0; i < len(md5s); i++ {
		var currentChunkSize int64
		if i == len(md5s)-1 {
			currentChunkSize = size - int64(i)*int64(chunkSize)
		} else {
			currentChunkSize = int64(chunkSize)
		}

		// Use SectionReader to read specific chunk
		// uploadSource must be io.ReaderAt (os.File satisfies this)
		sectionReader := io.NewSectionReader(uploadSource, int64(i)*int64(chunkSize), currentChunkSize)
		if _, err := o.sliceUpload(ctx, remote, uploadID, i, sectionReader, currentChunkSize); err != nil {
			return fmt.Errorf("slice %d upload failed: %w", i, err)
		}
	}

	md5ListBytes, _ := json.Marshal(md5s)
	file, err := o.complete(ctx, remote, uploadID, size, string(md5ListBytes))
	if err != nil {
		return fmt.Errorf("failed to merge file: %w", err)
	}
	o.id = strconv.FormatUint(file.FsID, 10)
	o.path = file.Path
	return nil
}

func (o *Object) precreate(ctx context.Context, remote string, size int64, md5s []string) (*PreUploadOut, error) {
	md5ListJSON, _ := json.Marshal(md5s)
	v := url.Values{}
	v.Set("path", remote)
	v.Set("size", strconv.FormatInt(size, 10))
	v.Set("isdir", "0")
	v.Set("autoinit", "1")
	v.Set("rtype", "3")
	v.Set("block_list", string(md5ListJSON))
	v.Set("openapi", "xpansdk")

	opts := &rest.Opts{
		Method:  "POST",
		RootURL: rootURL,
		Path:    uriFile,
		Parameters: map[string][]string{
			"method": {"precreate"},
		},
		Body:        strings.NewReader(v.Encode()),
		ContentType: "application/x-www-form-urlencoded",
	}
	resp := &PreUploadOut{}
	err := o.fs.call(ctx, opts, resp)
	return resp, err
}

// simpleUpload performs a single-part upload for files ≤4MB
func (o *Object) simpleUpload(ctx context.Context, in io.Reader, size int64) error {
	remote := o.fs.fullPath(o.remote)
	formReader, contentType, overhead, err := rest.MultipartUpload(ctx, in, nil, "file", "file", "")
	if err != nil {
		return err
	}
	contentLength := size + overhead
	opts := &rest.Opts{
		Method:        "POST",
		RootURL:       uploadURL,
		Path:          uriPCSFile,
		ContentType:   contentType,
		ContentLength: &contentLength,
		ExtraHeaders: map[string]string{
			"User-Agent": "netdisk;P2SP;8.3.1.2;PC;PC-Windows;10.0.19042;WindowsBaiduYunGuanJia",
		},
		Parameters: map[string][]string{
			"method":  {"upload"},
			"path":    {remote},
			"ondup":   {"overwrite"},
			"openapi": {"xpansdk"},
		},
		Body: formReader,
	}
	resp := SimpleUploadOut{}
	err = o.fs.call(ctx, opts, &resp)
	if err != nil {
		return err
	}
	o.id = strconv.FormatUint(resp.FsID, 10)
	o.path = resp.Path
	return nil
}

// sliceUpload uploads a single chunk of a multi-part upload
func (o *Object) sliceUpload(ctx context.Context, remote, uploadID string, partSeq int, in io.Reader, size int64) (md5sum string, err error) {
	formReader, contentType, overhead, err := rest.MultipartUpload(ctx, in, nil, "file", "file", "")
	if err != nil {
		return "", err
	}
	contentLength := size + overhead
	opts := &rest.Opts{
		Method:        "POST",
		RootURL:       uploadURL,
		Path:          uriSuperFile,
		ContentType:   contentType,
		ContentLength: &contentLength,
		ExtraHeaders: map[string]string{
			"User-Agent": "netdisk;P2SP;8.3.1.2;PC;PC-Windows;10.0.19042;WindowsBaiduYunGuanJia",
		},
		Parameters: map[string][]string{
			"method":   {"upload"},
			"type":     {"tmpfile"},
			"path":     {remote},
			"uploadid": {uploadID},
			"partseq":  {strconv.Itoa(partSeq)},
			"openapi":  {"xpansdk"},
		},
		Body: formReader,
	}

	resp := SliceUploadOut{}
	err = o.fs.call(ctx, opts, &resp)
	return resp.Md5, err
}

// complete merges all uploaded chunks into a single file
func (o *Object) complete(ctx context.Context, remote, uploadID string, size int64, md5ListJSON string) (FileEntity, error) {
	v := url.Values{}
	v.Set("path", remote)
	v.Set("size", strconv.FormatInt(size, 10))
	v.Set("isdir", "0")
	v.Set("rtype", "3")
	v.Set("uploadid", uploadID)
	// block_list must be a JSON array format: ["...", "..."]
	v.Set("block_list", md5ListJSON)
	v.Set("openapi", "xpansdk")

	opts := &rest.Opts{
		Method:  "POST",
		RootURL: rootURL,
		Path:    uriFile,
		Parameters: map[string][]string{
			"method": {"create"},
		},
		Body:        strings.NewReader(v.Encode()),
		ContentType: "application/x-www-form-urlencoded",
	}
	resp := MkdirOut{} // create API returns structure similar to mkdir
	err := o.fs.call(ctx, opts, &resp)
	if err != nil {
		return FileEntity{}, err
	}
	return FileEntity{
		FsID: resp.FsID,
		Path: resp.Path,
	}, nil
}

// Remove an object
func (o *Object) Remove(ctx context.Context) error {
	fileList := fmt.Sprintf(`[{"path":"%s"}]`, o.path)
	return o.fs.fileManager(ctx, "delete", fileList)
}

// ID returns the ID of the Object if known, or "" if not
func (o *Object) ID() string {
	return o.id
}

// setMetaData sets the metadata from info
func (o *Object) setMetaData(info *FileEntity) (err error) {
	if info.IsDir == 1 {
		return fs.ErrorIsDir
	}
	o.hasMetaData = true
	o.size = info.Size
	o.modTime = time.Unix(info.ServerMtime, 0)
	o.id = strconv.FormatUint(info.FsID, 10)
	o.path = info.Path
	o.remote = info.Path
	return nil
}

func (o *Object) readMetaData(ctx context.Context) (err error) {
	if o.hasMetaData {
		return nil
	}

	// XPAN cannot retrieve file metadata directly by path (errno: 2)
	// Must find the file through parent directory listing
	dir, leaf := path.Split(o.fs.fullPath(o.remote))
	list, err := o.fs.listDirAllFile(ctx, dir)
	if err != nil {
		return err
	}

	for _, v := range list {
		if v.IsDir == 0 && strings.EqualFold(v.ServerFilename, leaf) {
			return o.setMetaData(&v)
		}
	}

	return fs.ErrorObjectNotFound
}
