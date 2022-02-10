package hls

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "github.com/Monibuca/engine/v4"
	"github.com/Monibuca/engine/v4/codec/mpegts"
	"github.com/Monibuca/engine/v4/config"
	"github.com/Monibuca/engine/v4/util"
	. "github.com/Monibuca/plugin-ts/v4"
	"github.com/quangngotan95/go-m3u8/m3u8"
)

type HLSConfig struct {
	config.Publish
	config.Pull
	config.Subscribe
	Fragment     int64
	Window       int
	EnableWrite  bool   // 启动HLS写文件
	EnableMemory bool   // 启动内存模式
	Filter       string // 过滤，正则表达式
	Path         string // 存放路径
	filterReg    *regexp.Regexp
}

func (config *HLSConfig) Update(override config.Config) {
	if config.Filter != "" {
		config.filterReg = regexp.MustCompile(config.Filter)
	}
	Bus.Unsubscribe(Event_PUBLISH, config.writeHLS)
	if config.EnableWrite || config.EnableMemory {
		Bus.SubscribeAsync(Event_PUBLISH, config.writeHLS, false)
	}
}

func (config *HLSConfig) PullStream(streamPath string, puller Puller) bool {
	p := &HLSPuller{}
	p.Puller = puller
	var err error
	if p.Video.Req, err = http.NewRequest("GET", puller.RemoteURL, nil); err == nil {
		return p.Publish(streamPath, p, config.Publish)
	}
	return false
}

var hlsConfig = &HLSConfig{
	Fragment: 10,
	Window:   2,
	Path:     "hls",
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
	save := r.URL.Query().Get("save")
	p := new(HLSPuller)
	var err error
	p.Video.Req, err = http.NewRequest("GET", targetURL, nil)
	if err == nil {
		if !p.Publish(streamPath, p, config.Publish) {
			w.Write([]byte(`{"code":2,"msg":"bad name"}`))
			return
		}
		if save == "1" {
			if config.AutoPullList == nil {
				config.AutoPullList = make(map[string]string)
			}
			config.AutoPullList[streamPath] = targetURL
			if err = plugin.Save(); err != nil {
				plugin.Errorln(err)
			}
		}
		w.Write([]byte(`{"code":0}`))
	} else {
		w.Write([]byte(fmt.Sprintf(`{"code":1,"msg":"%s"}`, err.Error())))
	}
}

func (config *HLSConfig) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fileName := strings.TrimPrefix(r.URL.Path, "/hls/")
	if strings.HasSuffix(r.URL.Path, ".m3u8") {
		if f, err := os.Open(filepath.Join(hlsConfig.Path, fileName)); err == nil {
			w.Header().Add("Content-Type", "application/vnd.apple.mpegurl") //audio/x-mpegurl
			io.Copy(w, f)
			err = f.Close()
		} else if v, ok := memoryM3u8.Load(strings.TrimSuffix(fileName, ".m3u8")); ok {
			w.Header().Add("Content-Type", "application/vnd.apple.mpegurl") //audio/x-mpegurl
			buffer := v.(*bytes.Buffer)
			w.Write(buffer.Bytes())
		} else {
			w.WriteHeader(404)
		}
	} else if strings.HasSuffix(r.URL.Path, ".ts") {
		w.Header().Add("Content-Type", "video/mp2t") //video/mp2t
		tsPath := filepath.Join(hlsConfig.Path, fileName)
		if hlsConfig.EnableMemory {
			if tsData, ok := memoryTs.Load(tsPath); ok {
				buffers := net.Buffers{mpegts.DefaultPATPacket, mpegts.DefaultPMTPacket, tsData.([]byte)}
				buffers.WriteTo(w)
			} else {
				w.WriteHeader(404)
			}
		} else {
			if f, err := os.Open(tsPath); err == nil {
				io.Copy(w, f)
				err = f.Close()
			} else {
				w.WriteHeader(404)
			}
		}
	}
}

var plugin = InstallPlugin(hlsConfig)

// HLSPuller HLS拉流者
type HLSPuller struct {
	TSPuller
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
	M3u8Info  []TSCost      //每一个ts文件的消耗
}

// TSCost ts文件拉取的消耗信息
type TSCost struct {
	DownloadCost int
	DecodeCost   int
	BufferLength int
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
		plugin.Errorln("readM3U8 error:%s", err.Error())
	}
	return
}

func (p *HLSPuller) pull(info *M3u8Info) {
	//请求失败自动退出
	req := info.Req.WithContext(p)
	client := http.Client{Timeout: time.Second * 5}
	sequence := -1
	lastTs := make(map[string]bool)
	resp, err := client.Do(req)
	defer func() {
		plugin.Infof("hls %s exit:%v", p.Path, err)
		p.Close()
	}()
	errcount := 0
	for ; err == nil; resp, err = client.Do(req) {
		if playlist, err := readM3U8(resp); err == nil {
			errcount = 0
			info.LastM3u8 = playlist.String()
			//if !playlist.Live {
			//	log.Println(p.LastM3u8)
			//	return
			//}
			if playlist.Sequence <= sequence {
				plugin.Warnf("same sequence:%d,max:%d", playlist.Sequence, sequence)
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
			lastTs = thisTs
			if len(tsItems) > 3 {
				tsItems = tsItems[len(tsItems)-3:]
			}
			info.M3u8Info = nil
			for _, v := range tsItems {
				tsCost := TSCost{}
				tsUrl, _ := info.Req.URL.Parse(v.Segment)
				tsReq, _ := http.NewRequestWithContext(p, "GET", tsUrl.String(), nil)
				tsReq.Header = p.TsHead
				t1 := time.Now()
				if tsRes, err := client.Do(tsReq); err == nil {
					info.TSCount++
					p.Closer = tsReq.Body
					if body, err := ioutil.ReadAll(tsRes.Body); err == nil {
						tsCost.DownloadCost = int(time.Since(t1) / time.Millisecond)
						if p.SaveContext != nil && p.SaveContext.Err() == nil {
							os.MkdirAll(filepath.Join(hlsConfig.Path, p.Path), 0666)
							err = ioutil.WriteFile(filepath.Join(hlsConfig.Path, p.Path, filepath.Base(tsUrl.Path)), body, 0666)
						}
						t1 = time.Now()
						p.Reader = bytes.NewReader(body)
						p.TSPuller.Pull()
						tsCost.DecodeCost = int(time.Since(t1) / time.Millisecond)
					} else if err != nil {
						plugin.Errorln("%s readTs:%v", p.Path, err)
					}
				} else if err != nil {
					plugin.Errorln("%s reqTs:%v", p.Path, err)
				}
				info.M3u8Info = append(info.M3u8Info, tsCost)
			}

			time.Sleep(time.Second * time.Duration(playlist.Target) * 2)
		} else {
			plugin.Errorln("%s readM3u8:%v", p.Path, err)
			errcount++
			if errcount > 10 {
				return
			}
			//return
		}
	}
}

func (p *HLSPuller) Pull(count int) {
	go p.pull(&p.Video)
	if p.Audio.Req != nil {
		go p.pull(&p.Audio)
	}
}
