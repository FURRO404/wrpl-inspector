package wtcontent

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/dustin/go-humanize"
	"github.com/maxsupermanhd/lac/v2"
)

const contentTorrentLink = `https://yupmaster.gaijinent.com/yuitem/current_yup.php?project=warthunder&torrent=1`

// safe to call from multiple routines
// basically same as content directory but auto-downloads
// only use files folder through the torrenter to avoid partial files
type ContentTorrenter struct {
	cfg  lac.Conf
	lock sync.Mutex
	// files is the torrent indexed by game relative path, without the padding
	// files. It stays nil until the worker read the torrent.
	files    map[string]*torrent.File
	required map[string]bool
	pending  []string
	status   string
	// changed is closed and replaced every time the state moves. A waiter
	// keeps the channel it read, so a change that lands between the read and
	// the wait still releases it.
	changed chan struct{}
	// wake carries one token to the worker when a caller requires more files.
	wake chan struct{}
	done chan struct{}
}

func NewContentTorrenter(cfg lac.Conf) *ContentTorrenter {
	return &ContentTorrenter{
		cfg:      cfg,
		required: map[string]bool{},
		status:   "not started",
		changed:  make(chan struct{}),
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

// stores all files directly on disk
func (cont *ContentTorrenter) CfgDownloadDir() string {
	return cont.cfg.GetDString("gameContent/files", "filesPath")
}

// to not spam requests to yupmaster
func (cont *ContentTorrenter) CfgTorrentFileCache() string {
	return cont.cfg.GetDString("gameContent/current.torrent", "cachedTorrentPath")
}

func (cont *ContentTorrenter) CfgTorrentFileCacheLifetime() time.Duration {
	return time.Second * time.Duration(cont.cfg.GetDInt(60*60*24, "cachedTorrentLifetimeSeconds"))
}

// blocks if not ready, reads whole file from disk
// if file was not required, it needs to become required
// gives nil when it cannot, call Open for the reason
func (cont *ContentTorrenter) Get(name string) []byte {
	f, err := cont.Open(name)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	return b
}

// blocks if not ready, opens file from disk
// if file was not required, it needs to become required
func (cont *ContentTorrenter) Open(p string) (io.ReadSeekCloser, error) {
	if !cont.wait(context.Background(), cont.loaded) {
		return nil, errors.New("the torrenter stopped before it loaded the torrent")
	}
	cont.lock.Lock()
	_, ok := cont.files[p]
	cont.lock.Unlock()
	if !ok {
		return nil, fmt.Errorf("the torrent holds no file %q", p)
	}
	cont.Require([]string{p})
	if !cont.wait(context.Background(), func() bool { return cont.IsFileReady(p) }) {
		return nil, fmt.Errorf("the torrenter stopped before it downloaded %q", p)
	}
	return os.Open(filepath.Join(cont.CfgDownloadDir(), filepath.FromSlash(p)))
}

// a name is an exact game relative path or a path.Match pattern, such as
// "content*/*/res/locations_maps.dxp.bin"
func (cont *ContentTorrenter) Require(names []string) {
	cont.lock.Lock()
	cont.pending = append(cont.pending, names...)
	cont.lock.Unlock()
	select {
	case cont.wake <- struct{}{}:
	default: // the worker did not read the last token yet
	}
}

// Files lists the game relative paths the torrent holds. It is empty until the
// worker read the torrent.
func (cont *ContentTorrenter) Files() []string {
	cont.lock.Lock()
	defer cont.lock.Unlock()
	return slices.Sorted(maps.Keys(cont.files))
}

// MatchNames returns the names that match one of the patterns. A pattern
// without a wildcard matches one exact name.
func MatchNames(names, patterns []string) []string {
	var ret []string
	for _, n := range names {
		for _, p := range patterns {
			if ok, _ := path.Match(p, n); ok {
				ret = append(ret, n)
				break
			}
		}
	}
	return ret
}

// torrent was downloaded/fresh, hashes on disk of all existing files checked
// all required files are downloaded and on disk
func (cont *ContentTorrenter) IsReady() bool {
	if !cont.loaded() {
		return false
	}
	cont.lock.Lock()
	names := slices.Sorted(maps.Keys(cont.required))
	pending := len(cont.pending)
	cont.lock.Unlock()
	if pending > 0 {
		return false
	}
	for _, n := range names {
		if !cont.IsFileReady(n) {
			return false
		}
	}
	return true
}

// torrent was downloaded/fresh and this file has correct hash
//
// the worker checks the hash of every file on disk before it starts, and the
// storage writes into a ".part" file and renames it after every piece of the
// file is correct, so the final name with the full size is the proof
func (cont *ContentTorrenter) IsFileReady(name string) bool {
	cont.lock.Lock()
	f, ok := cont.files[name]
	cont.lock.Unlock()
	if !ok {
		return false
	}
	s, err := os.Stat(filepath.Join(cont.CfgDownloadDir(), filepath.FromSlash(name)))
	return err == nil && s.Size() == f.Length()
}

// returns true if all required files are ready, false if context was closed while waiting
func (cont *ContentTorrenter) WaitReady(ctx context.Context) bool {
	return cont.wait(ctx, cont.IsReady)
}

// same as IsFileReady but waits same as WaitReady
func (cont *ContentTorrenter) WaitFileReady(ctx context.Context, name string) bool {
	return cont.wait(ctx, func() bool { return cont.IsFileReady(name) })
}

// do literally everything in here, gracefully shutdown
func (cont *ContentTorrenter) Worker(ctx context.Context) {
	defer close(cont.done)
	mi, err := cont.metainfo()
	if err != nil {
		cont.setStatus("torrent file: " + err.Error())
		return
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		cont.setStatus("torrent info: " + err.Error())
		return
	}
	cont.setStatus("checking hashes")
	if err := cont.checkFiles(&info); err != nil {
		cont.setStatus("checking hashes: " + err.Error())
		return
	}
	cl, err := cont.client()
	if err != nil {
		cont.setStatus("torrent client: " + err.Error())
		return
	}
	defer cl.Close()
	t, err := cl.AddTorrent(mi)
	if err != nil {
		cont.setStatus("adding torrent: " + err.Error())
		return
	}
	select {
	case <-t.GotInfo():
	case <-ctx.Done():
		return
	}
	cont.setFiles(t.Files())
	cont.signal()

	// the torrent reports every piece that changes state, and that with a call
	// to Require are the only two things that move us, so we sleep between them
	pieces := t.SubscribePieceStateChanges()
	defer pieces.Close()
	last := int64(-1)
	for {
		cont.apply()
		done, want := cont.progress()
		if done != last {
			last = done
			state := "idle"
			if done < want {
				state = "downloading requirements"
			}
			cont.setStatus(fmt.Sprintf("%s, %s of %s", state,
				humanize.Bytes(uint64(done)), humanize.Bytes(uint64(want))))
		}
		// the byte count reaches the full size before the piece passes its
		// hash check, and that check is what renames the part file, so we
		// release the waiters on every event and not only on a count change
		cont.signal()
		select {
		case <-ctx.Done():
			return
		case <-cont.wake:
		case <-pieces.Values:
		}
	}
}

func (cont *ContentTorrenter) Status() string {
	/*
	   distinct states include:
	   - not started
	   - downloading torrent file
	   - checking hashes
	   - downloading requirements
	   - idle (ready to get more requirements when they arrive)
	*/
	cont.lock.Lock()
	defer cont.lock.Unlock()
	return cont.status
}

func (cont *ContentTorrenter) setStatus(s string) {
	cont.lock.Lock()
	cont.status = s
	cont.lock.Unlock()
}

// signal releases every waiter. The caller must not hold the lock.
func (cont *ContentTorrenter) signal() {
	cont.lock.Lock()
	close(cont.changed)
	cont.changed = make(chan struct{})
	cont.lock.Unlock()
}

// wait blocks until ready reports true. It returns false when the context ends,
// or when the worker stopped before the state became true.
func (cont *ContentTorrenter) wait(ctx context.Context, ready func() bool) bool {
	for {
		cont.lock.Lock()
		changed := cont.changed
		cont.lock.Unlock()
		if ready() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-cont.done:
			return ready()
		case <-changed:
		}
	}
}

// metainfo reads the cached torrent file. It gets a new one from yupmaster
// when the cache is missing or older than the lifetime.
func (cont *ContentTorrenter) metainfo() (*metainfo.MetaInfo, error) {
	p := cont.CfgTorrentFileCache()
	if s, err := os.Stat(p); err == nil && time.Since(s.ModTime()) < cont.CfgTorrentFileCacheLifetime() {
		return metainfo.LoadFromFile(p)
	}
	cont.setStatus("downloading torrent file")
	resp, err := http.Get(contentTorrentLink)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("torrent file %q: %s", contentTorrentLink, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		return nil, err
	}
	return metainfo.Load(bytes.NewReader(raw))
}

// checkFiles removes every file on disk that does not match the sha1 the
// torrent carries for it. The download directory holds only the files a caller
// required, so this reads what is there and not the whole torrent. It runs
// before the client opens the directory, so a removed file looks missing.
func (cont *ContentTorrenter) checkFiles(info *metainfo.Info) error {
	dir := cont.CfgDownloadDir()
	for _, fi := range info.UpvertedFiles() {
		if strings.Contains(fi.Attr, "p") || len(fi.Sha1) != sha1.Size {
			continue
		}
		p := filepath.Join(append([]string{dir}, fi.BestPath()...)...)
		s, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if s.Size() == fi.Length {
			sum, err := fileSha1(p)
			if err != nil {
				return err
			}
			if sum == fi.Sha1 {
				continue
			}
		}
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}

// fileSha1 is the digest of a file as 20 raw bytes, the form the torrent uses.
func fileSha1(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return string(h.Sum(nil)), nil
}

// client starts a torrent client that writes into the download directory. The
// default path maker of the file storage puts every file under a directory
// named after the torrent. The game expects the files at the root of the
// directory, so this replaces it.
func (cont *ContentTorrenter) client() (*torrent.Client, error) {
	dir := cont.CfgDownloadDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dir
	cfg.Seed = cont.cfg.GetDBool(true, "seed")
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: dir,
		FilePathMaker: func(o storage.FilePathMakerOpts) string {
			return filepath.Join(o.File.BestPath()...)
		},
	})
	return torrent.NewClient(cfg)
}

// setFiles indexes the torrent files by their game relative path. It drops the
// BEP 47 padding files, which hold no game data.
func (cont *ContentTorrenter) setFiles(files []*torrent.File) {
	m := make(map[string]*torrent.File, len(files))
	for _, f := range files {
		if strings.Contains(f.FileInfo().Attr, "p") {
			continue
		}
		m[f.DisplayPath()] = f
	}
	cont.lock.Lock()
	cont.files = m
	cont.lock.Unlock()
}

// loaded reports that the worker read the torrent and indexed its files.
func (cont *ContentTorrenter) loaded() bool {
	cont.lock.Lock()
	defer cont.lock.Unlock()
	return cont.files != nil
}

// apply starts the download of every file that matches a new pattern.
func (cont *ContentTorrenter) apply() {
	cont.lock.Lock()
	defer cont.lock.Unlock()
	if len(cont.pending) == 0 || cont.files == nil {
		return
	}
	for _, n := range MatchNames(slices.Sorted(maps.Keys(cont.files)), cont.pending) {
		if cont.required[n] {
			continue
		}
		cont.required[n] = true
		cont.files[n].Download()
	}
	cont.pending = nil
}

// progress sums the size of the required files and how much of them is on
// disk.
func (cont *ContentTorrenter) progress() (done, want int64) {
	cont.lock.Lock()
	defer cont.lock.Unlock()
	for n := range cont.required {
		done += cont.files[n].BytesCompleted()
		want += cont.files[n].Length()
	}
	return done, want
}

// ContentTorrenter reads game files the same way a game directory does.
var _ GameContentProvider = (*ContentTorrenter)(nil)
