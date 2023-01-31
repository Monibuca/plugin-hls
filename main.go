package hls // import "m7s.live/plugin/hls/v4"

import (
	"compress/gzip"
	"container/ring"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/quangngotan95/go-m3u8/m3u8"
	"go.uber.org/zap"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/config"
	"m7s.live/engine/v4/util"
)

//go:embed default.ts
var defaultTS []byte
var defaultSeq = 0 // 默认片头的全局序号
var writing = make(map[string]*HLSWriter)

type HLSConfig struct {
	config.Publish
	config.Pull
	config.Subscribe
	Fragment          time.Duration
	Window            int
	Filter            string // 过滤，正则表达式
	Path              string
	DefaultTS         string        // 默认的ts文件
	DefaultTSDuration time.Duration // 默认的ts文件时长(秒)
	filterReg         *regexp.Regexp
}

func (c *HLSConfig) OnEvent(event any) {
	switch v := event.(type) {
	case FirstConfig:
		if c.Filter != "" {
			c.filterReg = regexp.MustCompile(c.Filter)
		}
		for streamPath, url := range c.PullOnStart {
			if err := HLSPlugin.Pull(streamPath, url, new(HLSPuller), 0); err != nil {
				HLSPlugin.Error("pull", zap.String("streamPath", streamPath), zap.String("url", url), zap.Error(err))
			}
		}
		if c.DefaultTS != "" {
			ts, err := os.ReadFile(c.DefaultTS)
			if err == nil {
				defaultTS = ts
			} else {
				log.Panic("read default ts error")
			}
		} else {
			c.DefaultTSDuration = time.Second * 388 / 100
		}
		if c.DefaultTSDuration == 0 {
			log.Panic("default ts duration error")
		} else {
			go func() {
				ticker := time.NewTicker(c.DefaultTSDuration)
				for range ticker.C {
					defaultSeq++
				}
			}()
		}
	case config.Config:
		if c.Filter != "" {
			c.filterReg = regexp.MustCompile(c.Filter)
		}
	case SEclose:
		delete(writing, v.Stream.Path)
	case SEpublish:
		if writing[v.Stream.Path] == nil && (c.filterReg == nil || c.filterReg.MatchString(v.Stream.Path)) {
			var outStream = HLSWriter{
				infoRing: ring.New(c.Window),
			}
			writing[v.Stream.Path] = &outStream
			go outStream.Start(v.Stream)
		}
	case *Stream: //按需拉流
		for streamPath, url := range c.PullOnSub {
			if streamPath == v.Path {
				if err := HLSPlugin.Pull(streamPath, url, new(HLSPuller), 0); err != nil {
					HLSPlugin.Error("pull", zap.String("streamPath", streamPath), zap.String("url", url), zap.Error(err))
				}
				break
			}
		}
	}
}

var hlsConfig = &HLSConfig{
	Fragment: time.Second * 10,
	Window:   2,
}

func (config *HLSConfig) API_List(w http.ResponseWriter, r *http.Request) {
	util.ReturnJson(FilterStreams[*HLSPuller], time.Second, w, r)
}

// 处于拉流时，可以调用这个API将拉流的TS文件保存下来，这个http如果断开，则停止保存
func (config *HLSConfig) API_Save(w http.ResponseWriter, r *http.Request) {
	streamPath := r.URL.Query().Get("streamPath")
	if s := Streams.Get(streamPath); s != nil {
		if hls, ok := s.Publisher.(*HLSPuller); ok {
			hls.SaveContext = r.Context()
			<-hls.SaveContext.Done()
		}
	}
}

func (config *HLSConfig) API_Pull(w http.ResponseWriter, r *http.Request) {
	targetURL := r.URL.Query().Get("target")
	streamPath := r.URL.Query().Get("streamPath")
	save, _ := strconv.Atoi(r.URL.Query().Get("save"))
	if err := HLSPlugin.Pull(streamPath, targetURL, new(HLSPuller), save); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	} else {
		w.Write([]byte("ok"))
	}
}

func (config *HLSConfig) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fileName := strings.TrimPrefix(r.URL.Path, "/hls")
	fileName = strings.TrimPrefix(fileName, "/")
	if strings.HasSuffix(r.URL.Path, ".m3u8") {
		w.Header().Add("Content-Type", "application/vnd.apple.mpegurl")
		if v, ok := memoryM3u8.Load(strings.TrimSuffix(fileName, ".m3u8")); ok {
			switch hls := v.(type) {
			case *HLSWriter:
				hls.RLock()
				w.Write(hls.Bytes())
				hls.RUnlock()
				return
			case string:
				if _, ok := memoryM3u8.Load(hls); ok {
					ss := strings.Split(hls, "/")
					m3u8 := fmt.Sprintf(`#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=2560000
%s/%s.m3u8
					`, ss[len(ss)-2], ss[len(ss)-1])
					w.Write([]byte(m3u8))
					return
				}
			}
		}
		w.Write([]byte(fmt.Sprintf(`#EXTM3U
#EXT-X-VERSION:3
#EXT-X-MEDIA-SEQUENCE:%d
#EXT-X-TARGETDURATION:%d
#EXT-X-DISCONTINUITY-SEQUENCE:%d
#EXT-X-DISCONTINUITY
#EXTINF:%.3f,
default.ts`, defaultSeq, int(math.Ceil(float64(config.DefaultTSDuration/time.Second))), defaultSeq, config.DefaultTSDuration)))
	} else if strings.HasSuffix(r.URL.Path, ".ts") {
		w.Header().Add("Content-Type", "video/mp2t") //video/mp2t
		if tsData, ok := memoryTs.Load(fileName); ok {
			tsInfo := tsData.(net.Buffers)
			var data net.Buffers
			data = append(data, tsInfo...)
			data.WriteTo(w)
		} else {
			w.Write(defaultTS)
			// w.WriteHeader(http.StatusNotFound)
		}
	}
}

var HLSPlugin = InstallPlugin(hlsConfig)

// HLSPuller HLS拉流者
type HLSPuller struct {
	TSPublisher
	Puller
	Video       M3u8Info
	Audio       M3u8Info
	TsHead      http.Header     `json:"-"` //用于提供cookie等特殊身份的http头
	SaveContext context.Context `json:"-"` //用来保存ts文件到服务器
}

// M3u8Info m3u8文件的信息，用于拉取m3u8文件，和提供查询
type M3u8Info struct {
	Req       *http.Request `json:"-"`
	M3U8Count int           //一共拉取的m3u8文件数量
	TSCount   int           //一共拉取的ts文件数量
	LastM3u8  string        //最后一个m3u8文件内容
}

func readM3U8(res *http.Response) (playlist *m3u8.Playlist, err error) {
	var reader io.Reader = res.Body
	if res.Header.Get("Content-Encoding") == "gzip" {
		reader, err = gzip.NewReader(reader)
	}
	if err == nil {
		playlist, err = m3u8.Read(reader)
	}
	if err != nil {
		HLSPlugin.Error("readM3U8", zap.Error(err))
	}
	return
}

func (p *HLSPuller) pull(info *M3u8Info) error {
	//请求失败自动退出
	req := info.Req.WithContext(p.Context)
	client := http.Client{}
	sequence := -1
	lastTs := make(map[string]bool)
	tsbuffer := make(chan io.ReadCloser)
	resp, err := client.Do(req)
	defer func() {
		HLSPlugin.Info("hls exit", zap.String("streamPath", p.Stream.Path), zap.Error(err))
		defer close(tsbuffer)
		p.Stop()
	}()
	for errcount := 0; err == nil; resp, err = client.Do(req) {
		if playlist, err := readM3U8(resp); err == nil {
			errcount = 0
			info.LastM3u8 = playlist.String()
			//if !playlist.Live {
			//	log.Println(p.LastM3u8)
			//	return
			//}
			if playlist.Sequence <= sequence {
				HLSPlugin.Warn("same sequence", zap.Int("sequence", playlist.Sequence), zap.Int("max", sequence))
				time.Sleep(time.Second)
				continue
			}
			info.M3U8Count++
			sequence = playlist.Sequence
			thisTs := make(map[string]bool)
			tsItems := make([]*m3u8.SegmentItem, 0)
			discontinuity := false
			for _, item := range playlist.Items {
				switch v := item.(type) {
				case *m3u8.DiscontinuityItem:
					discontinuity = true
				case *m3u8.SegmentItem:
					thisTs[v.Segment] = true
					if _, ok := lastTs[v.Segment]; ok && !discontinuity {
						continue
					}
					tsItems = append(tsItems, v)
				}
			}
			HLSPlugin.Debug("readM3U8", zap.Int("sequence", sequence), zap.Int("tscount", len(tsItems)))
			lastTs = thisTs
			if len(tsItems) > 3 {
				tsItems = tsItems[len(tsItems)-3:]
			}
			for _, v := range tsItems {
				tsUrl, _ := info.Req.URL.Parse(v.Segment)
				tsReq, _ := http.NewRequestWithContext(p.Context, "GET", tsUrl.String(), nil)
				tsReq.Header = p.TsHead
				// t1 := time.Now()
				HLSPlugin.Debug("start download ts", zap.String("tsUrl", tsUrl.String()))
				if tsRes, err := client.Do(tsReq); err == nil {
					info.TSCount++
					p.SetIO(tsRes.Body)
					if p.SaveContext != nil && p.SaveContext.Err() == nil {
						os.MkdirAll(filepath.Join(hlsConfig.Path, p.Stream.Path), 0766)
						if f, err := os.OpenFile(filepath.Join(hlsConfig.Path, p.Stream.Path, filepath.Base(tsUrl.Path)), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666); err == nil {
							p.SetIO(io.TeeReader(tsRes.Body, f))
							p.Closer = f
						}
					}
					p.Feed(p)
					p.Close()
				} else if err != nil {
					HLSPlugin.Error("reqTs", zap.String("streamPath", p.Stream.Path), zap.Error(err))
				}
			}
		} else {
			HLSPlugin.Error("readM3u8", zap.String("streamPath", p.Stream.Path), zap.Error(err))
			errcount++
			if errcount > 10 {
				return err
			}
		}
	}
	return err
}

func (p *HLSPuller) Connect() (err error) {
	p.Video.Req, err = http.NewRequest("GET", p.RemoteURL, nil)
	return
}

func (p *HLSPuller) Pull() error {
	if p.Audio.Req != nil {
		go p.pull(&p.Audio)
	}
	return p.pull(&p.Video)
}
