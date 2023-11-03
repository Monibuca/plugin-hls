package hls

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/quangngotan95/go-m3u8/m3u8"
	"go.uber.org/zap"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/util"
)

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

type TSDownloader struct {
	client *http.Client
	url    *url.URL
	req    *http.Request
	res    *http.Response
	wg     sync.WaitGroup
	err    error
	dur    float64
}

func (p *TSDownloader) Start() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		if tsRes, err := p.client.Do(p.req); err == nil {
			p.res = tsRes
		} else {
			p.err = err
		}
	}()
}

func (p *HLSPuller) GetTs(key string) util.Recyclable {
	return p.memoryTs.Get(key)
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
			memoryTs.Add(p.StreamPath, p)
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
		resp, err1 := client.Do(req)
		if err1 != nil {
			return err1
		}
		req = resp.Request
		if playlist, err2 := readM3U8(resp); err2 == nil {
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
			tsCount := len(tsItems)
			HLSPlugin.Debug("readM3U8", zap.Int("sequence", sequence), zap.Int("tscount", tsCount))
			lastTs = thisTs
			if tsCount > 3 {
				tsItems = tsItems[tsCount-3:]
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
			var tsDownloaders = make([]*TSDownloader, len(tsItems))
			for i, v := range tsItems {
				if p.Err() != nil {
					return p.Err()
				}
				tsUrl, _ := info.Req.URL.Parse(v.Segment)
				tsReq, _ := http.NewRequestWithContext(p.Context, "GET", tsUrl.String(), nil)
				tsReq.Header = p.TsHead
				// t1 := time.Now()
				tsDownloaders[i] = &TSDownloader{
					client: client,
					req:    tsReq,
					url:    tsUrl,
					dur:    v.Duration,
				}
				tsDownloaders[i].Start()
			}
			ts := time.Now().UnixMilli()
			for i, v := range tsDownloaders {
				HLSPlugin.Debug("start download ts", zap.String("tsUrl", v.url.String()))
				v.wg.Wait()
				if v.res != nil {
					info.TSCount++
					p.SetIO(v.res.Body)
					if p.SaveContext != nil && p.SaveContext.Err() == nil {
						os.MkdirAll(filepath.Join(hlsConfig.Path, p.Stream.Path), 0766)
						if f, err := os.OpenFile(filepath.Join(hlsConfig.Path, p.Stream.Path, filepath.Base(v.url.Path)), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666); err == nil {
							p.SetIO(io.TeeReader(v.res.Body, f))
							p.Closer = f
						}
					}
					var tsBytes *util.Buffer
					var item *util.ListItem[util.Buffer]
					// 包含转发
					if hlsConfig.RelayMode != 0 {
						if v.res.ContentLength < 0 {
							item = bytesPool.GetShell(make([]byte, 0))
						} else {
							item = bytesPool.Get(int(v.res.ContentLength))
							item.Value = item.Value[:0]
						}
						tsBytes = &item.Value
					}
					switch hlsConfig.RelayMode {
					case 1:
						io.Copy(tsBytes, p.Reader)
					case 2:
						p.SetIO(io.TeeReader(p.Reader, tsBytes))
						fallthrough
					case 0:
						tsReader.Feed(p)
					}
					if hlsConfig.RelayMode != 0 {
						tsFilename := fmt.Sprintf("%d_%d.ts", ts, i)
						tsFilePath := p.StreamPath + "/" + tsFilename
						var plInfo = PlaylistInf{
							Title:    p.Stream.StreamName + "/" + tsFilename,
							Duration: v.dur,
							FilePath: tsFilePath,
						}
						relayPlayList.WriteInf(plInfo)
						p.memoryTs.Add(tsFilePath, item)
						next := tsRing.Next()
						if next.Value != "" {
							item, _ := p.memoryTs.Delete(next.Value)
							if item == nil {
								p.Warn("memoryTs delete nil", zap.String("tsFilePath", next.Value))
							} else {
								item.Recycle()
							}
						}
						next.Value = tsFilePath
						tsRing = next
					}
					p.Close()
				} else if v.err != nil {
					HLSPlugin.Error("reqTs", zap.String("streamPath", p.Stream.Path), zap.Error(v.err))
				} else {
					HLSPlugin.Error("reqTs", zap.String("streamPath", p.Stream.Path))
				}
				HLSPlugin.Debug("finish download ts", zap.String("tsUrl", v.url.String()))
			}
			if hlsConfig.RelayMode != 0 {
				m3u8 := string(plBuffer)
				HLSPlugin.Debug("write m3u8", zap.String("streamPath", p.Stream.Path), zap.String("m3u8", m3u8))
				memoryM3u8.Store(p.Stream.Path, m3u8)
			}
		} else {
			HLSPlugin.Error("readM3u8", zap.String("streamPath", p.Stream.Path), zap.Error(err2))
			errcount++
			if errcount > 10 {
				return err2
			}
		}
	}
	return
}

func (p *HLSPuller) Connect() (err error) {
	p.Video.Req, err = http.NewRequest("GET", p.RemoteURL, nil)
	return
}

func (p *HLSPuller) Disconnect() {
	p.Stop(zap.String("reason", "disconnect"))
}

func (p *HLSPuller) Pull() error {
	return p.pull(&p.Video)
}
