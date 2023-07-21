package hls // import "m7s.live/plugin/hls/v4"

import (
	"compress/gzip"
	"context"
	"embed"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
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

//go:embed hls.js
var hls_js embed.FS

//go:embed default.ts
var defaultTS []byte

//go:embed default.yaml
var defaultYaml DefaultYaml
var defaultSeq = 0 // 默认片头的全局序号
var writing = make(map[string]*HLSWriter)
var hlsConfig = &HLSConfig{
	DefaultYaml: defaultYaml,
}
var HLSPlugin = InstallPlugin(hlsConfig)

type HLSConfig struct {
	DefaultYaml
	config.HTTP
	config.Publish
	config.Pull
	config.Subscribe
	Fragment          time.Duration `default:"2s"`
	Window            int           `default:"3"`
	Filter            string        // 过滤，正则表达式
	Path              string
	DefaultTS         string        // 默认的ts文件
	DefaultTSDuration time.Duration // 默认的ts文件时长(秒)
	RelayMode         int           // 转发模式,0:转协议+不转发,1:不转协议+转发，2:转协议+转发
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
		delete(writing, v.Target.Path)
	case SEpublish:
		if writing[v.Target.Path] == nil && (c.filterReg == nil || c.filterReg.MatchString(v.Target.Path)) {
			if _, ok := v.Target.Publisher.(*HLSPuller); !ok || hlsConfig.RelayMode == 0 {
				var outStream HLSWriter
				writing[v.Target.Path] = &outStream
				go outStream.Start(v.Target)
			}
		}
	case *Stream: //按需拉流
		if url, ok := c.PullOnSub[v.Path]; ok {
			if err := HLSPlugin.Pull(v.Path, url, new(HLSPuller), 0); err != nil {
				HLSPlugin.Error("pull", zap.String("streamPath", v.Path), zap.String("url", url), zap.Error(err))
			}
		}
	}
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
	fileName := strings.TrimPrefix(r.URL.Path, "/")
	if strings.HasSuffix(r.URL.Path, ".m3u8") {
		w.Header().Add("Content-Type", "application/vnd.apple.mpegurl")
		if v, ok := memoryM3u8.Load(strings.TrimSuffix(fileName, ".m3u8")); ok {
			switch hls := v.(type) {
			case *TrackReader:
				hls.RLock()
				w.Write(hls.M3u8)
				hls.RUnlock()
				return
			case string:
				w.Write([]byte(hls))
				return
				// 				if _, ok := memoryM3u8.Load(hls); ok {
				// 					ss := strings.Split(hls, "/")
				// 					m3u8 := fmt.Sprintf(`#EXTM3U
				// #EXT-X-VERSION:3
				// #EXT-X-STREAM-INF:BANDWIDTH=2560000
				// %s/%s.m3u8
				// 					`, ss[len(ss)-2], ss[len(ss)-1])
				// 					w.Write([]byte(m3u8))
				// 					return
				// 				}
			}
		}
		w.Write([]byte(fmt.Sprintf(`#EXTM3U
#EXT-X-VERSION:3
#EXT-X-MEDIA-SEQUENCE:%d
#EXT-X-TARGETDURATION:%d
#EXT-X-DISCONTINUITY-SEQUENCE:%d
#EXT-X-DISCONTINUITY
#EXTINF:%.3f,
default.ts`, defaultSeq, int(math.Ceil(config.DefaultTSDuration.Seconds())), defaultSeq, config.DefaultTSDuration.Seconds())))
	} else if strings.HasSuffix(r.URL.Path, ".ts") {
		w.Header().Add("Content-Type", "video/mp2t") //video/mp2t
		streamPath := path.Dir(fileName)
		tsData := memoryTs.Get(streamPath)
		if tsData == nil {
			tsData = memoryTs.Get(path.Dir(streamPath))
			if tsData == nil {
				w.Write(defaultTS)
				return
			}
		}
		if tsData := tsData.Get(fileName); tsData != nil {
			switch v := tsData.(type) {
			case *MemoryTs:
				v.WriteTo(w)
			case *util.ListItem[util.Buffer]:
				w.Write(v.Value)
			}
		} else {
			w.Write(defaultTS)
		}
	} else {
		f, err := hls_js.ReadFile("hls.js/" + fileName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			w.Write(f)
		}
		// if file, err := hls_js.Open(fileName); err == nil {
		// 	defer file.Close()
		// 	if info, err := file.Stat(); err == nil {
		// 		http.ServeContent(w, r, fileName, info.ModTime(), file)
		// 	}
		// } else {
		// 	http.NotFound(w, r)
		// }
	}
}

// HLSPuller HLS拉流者
type HLSPuller struct {
	TSPublisher
	Puller
	Video       M3u8Info
	Audio       M3u8Info
	TsHead      http.Header     `json:"-" yaml:"-"` //用于提供cookie等特殊身份的http头
	SaveContext context.Context `json:"-" yaml:"-"` //用来保存ts文件到服务器
	memoryTs    util.Map[string, util.Recyclable]
}

// M3u8Info m3u8文件的信息，用于拉取m3u8文件，和提供查询
type M3u8Info struct {
	Req       *http.Request `json:"-" yaml:"-"`
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
func (p *HLSPuller) OnEvent(event any) {
	switch event.(type) {
	case IPublisher:
		p.TSPublisher.OnEvent(event)
		if hlsConfig.RelayMode != 0 {
			p.Stream.NeverTimeout = true
			memoryTs.Add(p.StreamPath, &p.memoryTs)
		}
	case SEKick, SEclose:
		if hlsConfig.RelayMode == 1 {
			memoryTs.Delete(p.StreamPath)
		}
		p.Publisher.OnEvent(event)
	default:
		p.Publisher.OnEvent(event)
	}
}
func (p *HLSPuller) pull(info *M3u8Info) (err error) {
	//请求失败自动退出
	req := info.Req.WithContext(p.Context)
	client := http.DefaultClient
	if p.Puller.Config.Proxy != "" {
		URL, err := url.Parse(p.Puller.Config.Proxy)
		if err != nil {
			return err
		}
		client = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(URL),
			},
		}
	}
	sequence := -1
	lastTs := make(map[string]bool)
	tsbuffer := make(chan io.ReadCloser)
	bytesPool := make(util.BytesPool, 30)
	tsRing := util.NewRing[string](6)
	var tsReader *TSReader
	if hlsConfig.RelayMode != 1 {
		tsReader = NewTSReader(&p.TSPublisher)
		defer tsReader.Close()
	}
	defer func() {
		HLSPlugin.Info("hls exit", zap.String("streamPath", p.Stream.Path), zap.Error(err))
		close(tsbuffer)
		p.Stop()
	}()
	var maxResolution *m3u8.PlaylistItem
	for errcount := 0; err == nil; err = p.Err() {
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		req = resp.Request
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
				case *m3u8.PlaylistItem:
					if (maxResolution == nil || maxResolution.Resolution != nil && (maxResolution.Resolution.Width < v.Resolution.Width || maxResolution.Resolution.Height < v.Resolution.Height)) || maxResolution.Bandwidth < v.Bandwidth {
						maxResolution = v
					}
				case *m3u8.DiscontinuityItem:
					discontinuity = true
				case *m3u8.SegmentItem:
					thisTs[v.Segment] = true
					if _, ok := lastTs[v.Segment]; ok && !discontinuity {
						continue
					}
					tsItems = append(tsItems, v)
				case *m3u8.MediaItem:
					if p.Audio.Req == nil {
						if url, err := req.URL.Parse(*v.URI); err == nil {
							newReq, _ := http.NewRequest("GET", url.String(), nil)
							newReq.Header = req.Header
							p.Audio.Req = newReq
							go p.pull(&p.Audio)
						}
					}
				}
			}
			if maxResolution != nil && len(tsItems) == 0 {
				if url, err := req.URL.Parse(maxResolution.URI); err == nil {
					if strings.HasSuffix(url.Path, ".m3u8") {
						p.Video.Req, _ = http.NewRequest("GET", url.String(), nil)
						p.Video.Req.Header = req.Header
						req = p.Video.Req
						continue
					}
				}
			}
			HLSPlugin.Debug("readM3U8", zap.Int("sequence", sequence), zap.Int("tscount", len(tsItems)))
			lastTs = thisTs
			if len(tsItems) > 3 {
				tsItems = tsItems[len(tsItems)-3:]
			}
			var plBuffer util.Buffer
			relayPlayList := Playlist{
				Writer:         &plBuffer,
				Targetduration: playlist.Target,
				Sequence:       playlist.Sequence,
			}
			if hlsConfig.RelayMode != 0 {
				relayPlayList.Init()
			}
			for _, v := range tsItems {
				if p.Err() != nil {
					return p.Err()
				}
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
					var tsBytes util.LimitBuffer
					var item *util.ListItem[util.Buffer]
					// 包含转发
					if hlsConfig.RelayMode != 0 {
						item = bytesPool.Get(int(tsRes.ContentLength))
						tsBytes.Buffer = item.Value[:0]
					}
					// 双转
					if hlsConfig.RelayMode == 2 {
						p.SetIO(io.TeeReader(p.Reader, &tsBytes))
					}
					// 包含转协议
					if hlsConfig.RelayMode != 1 {
						tsReader.Feed(p)
					} else {
						io.Copy(&tsBytes, p.Reader)
					}
					if hlsConfig.RelayMode != 0 {
						tsFilename := strconv.FormatInt(time.Now().Unix(), 10) + ".ts"
						tsFilePath := p.StreamPath + "/" + tsFilename
						var plInfo = PlaylistInf{
							Title:    p.Stream.StreamName + "/" + tsFilename,
							Duration: v.Duration,
							FilePath: tsFilePath,
						}
						relayPlayList.WriteInf(plInfo)
						p.memoryTs.Add(tsFilePath, item)
						next := tsRing.Next()
						if next.Value != "" {
							item, _ := p.memoryTs.Delete(next.Value)
							if item == nil {
								p.Warn("memoryTs delete nil", zap.String("streamPath", p.Stream.Path), zap.String("tsFilePath", next.Value))
							} else {
								item.(*util.ListItem[util.Buffer]).Recycle()
							}
						}
						next.Value = tsFilePath
						tsRing = next
					}
					p.Close()
				} else if err != nil {
					HLSPlugin.Error("reqTs", zap.String("streamPath", p.Stream.Path), zap.Error(err))
				}
			}
			if hlsConfig.RelayMode != 0 {
				memoryM3u8.Store(p.Stream.Path, string(plBuffer))
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
	return p.pull(&p.Video)
}
