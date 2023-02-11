# HLS插件

- 该插件可用来拉取网络上的m3u8文件并解析后转换成其他协议
- 可以直接访问`http://localhost:8080/hls/live/user1.m3u8` 进行播放，其中8080端口是全局HTTP配置，live/user1是streamPath，需要根据实际情况修改

## 插件地址

https://github.com/Monibuca/plugin-hls

## 插件引入
```go
import (
    _ "m7s.live/plugin/hls/v4"
)
```

## API
> 参数是可变的，下面的参数live/hls是作为例子，不是固定的
- `/hls/api/list`
列出所有HLS流，是一个SSE，可以持续接受到列表数据，加上?json=1 可以返回json数据。
- `/hls/api/save?streamPath=live/hls`
保存指定的流（例如live/hls）为HLS文件（m3u8和ts）当这个请求关闭时就结束保存（该API仅作用于远程拉流）
- `/hls/api/pull?streamPath=live/hls&target=http://localhost/abc.m3u8`
将目标HLS流拉过来作为媒体源在monibuca内以`live/hls`流的形式存在
## 配置
- 配置信息按照需要添加到配置文件中，无需复制全部默认配置信息
- publish 和 subscribe 配置会覆盖全局配置
```yaml
hls:
    publish:
        pubaudio: true # 是否发布音频流
        pubvideo: true # 是否发布视频流
        kickexist: false # 剔出已经存在的发布者，用于顶替原有发布者
        publishtimeout: 10s # 发布流默认过期时间，超过该时间发布者没有恢复流将被删除
        delayclosetimeout: 0 # 自动关闭触发后延迟的时间(期间内如果有新的订阅则取消触发关闭)，0为关闭该功能，保持连接。
        waitclosetimeout: 0 # 发布者断开后等待时间，超过该时间发布者没有恢复流将被删除，0为关闭该功能，由订阅者决定是否删除
        buffertime: 0 # 缓存时间，用于时光回溯，0为关闭缓存
    subscribe:
        subaudio: true # 是否订阅音频流
        subvideo: true # 是否订阅视频流
        subaudioargname: ats # 订阅音频轨道参数名
        subvideoargname: vts # 订阅视频轨道参数名
        subdataargname: dts # 订阅数据轨道参数名
        subaudiotracks: [] # 订阅音频轨道名称列表
        subvideotracks: [] # 订阅视频轨道名称列表
        submode: 0 # 订阅模式，0为跳帧追赶模式，1为不追赶（多用于录制），2为时光回溯模式
        iframeonly: false # 只订阅关键帧
        waittimeout: 10s # 等待发布者的超时时间，用于订阅尚未发布的流
    pull:
        repull: 0
        pullonstart: {} # 服务启动时自动拉流
        pullonsub: {} # 订阅时自动拉流(按需拉流)
    fragment: 10s # TS分片长度
    window: 2 # 实时流m3u8文件包含的TS文件数
    filter: "" # 正则表达式，用来过滤发布的流，只有匹配到的流才会写入
    path: "" # 远端拉流如果需要保存的话，存放的目录
    defaultts: "" # 默认切片用于无流时片头播放,如果留空则使用系统内置
    defaulttsduration: 3.88s # 默认切片的长度
```