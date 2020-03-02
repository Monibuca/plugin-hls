<template>
    <div class="layout">
        <Card v-for="item in Rooms" :key="item.TSInfo.RoomInfo.StreamPath" class="room">
            <p slot="title">{{item.TSInfo.RoomInfo.StreamPath}}</p>
            <StartTime slot="extra" :value="item.TSInfo.RoomInfo.StartTime"></StartTime>
            <div class="hls-info">
                <div>
                    <Badge :count="Number(item.Provider)" />
                    <StartTime :value="item.TSInfo.RoomInfo.StartTime" />
                    <Tooltip
                        :content="item.TSInfo.BufferLength+'/2048'"
                        style="width: 240px"
                    >
                        <Progress
                            :stroke-width="20"
                            :percent="Math.ceil(item.TSInfo.BufferLength*100/2048)"
                            text-inside
                        />
                    </Tooltip>
                </div>
                <div>
                    <Poptip trigger="hover">
                        <table class="ts-info" slot="content">
                            <tr v-for="(tsInfo,index) in item.Audio.M3u8Info" :key="index">
                                <td v-for="(v,k) in tsInfo" :key="k">{{v}}</td>
                            </tr>
                        </table>
                        📑 {{item.Audio.M3U8Count}}
                    </Poptip>|
                    <Poptip trigger="hover">
                        <table class="ts-info" slot="content">
                            <tr v-for="(tsInfo,index) in item.Video.M3u8Info" :key="index">
                                <td v-for="(v,k) in tsInfo" :key="k">{{v}}</td>
                            </tr>
                        </table>
                        {{item.Video.M3U8Count}}
                    </Poptip>
                    💿 {{item.Audio.TSCount}}|{{item.Video.TSCount}} 📜
                    {{item.TSInfo.TotalPesCount}}
                    📼
                    {{item.TSInfo.RoomInfo.AudioInfo.PacketCount}} 📺
                    {{item.TSInfo.RoomInfo.VideoInfo.PacketCount}}
                </div>
            </div>
            <ButtonGroup>
                <Button @click="showIndexM3u8(item)">📃Index</Button>
                <Button @click="showAudioM3u8(item)" v-if="item.Audio.LastM3u8.length">📑Audio</Button>
                <Button @click="showVideoM3u8(item)">📑Video</Button>
                <Button @click="saveTs(item)">💾Save</Button>
            </ButtonGroup>
        </Card>
        <div v-if="Rooms.length==0" class="empty">
            <Icon type="md-wine" size="50" />没有任何房间
        </div>
    </div>
</template>

<script>
let listES = null;
import StartTime from "./components/StartTime"
export default {
    components:{
        StartTime,
    },
    data() {
        return {
            currentStream: null,
            Rooms: []
        };
    },

    methods: {
        showIndexM3u8(item) {
            this.$Modal.info({
                title: "IndexM3u8",
                width: "1000px",
                scrollable: true,
                content: item.MasterM3u8
            });
        },
        showAudioM3u8(item) {
            this.$Modal.info({
                title: "AudioM3u8",
                width: "1000px",
                scrollable: true,
                content: item.Audio.LastM3u8
            });
        },
        showVideoM3u8(item) {
            this.$Modal.info({
                title: "VideoM3u8",
                width: "1000px",
                scrollable: true,
                content: item.Video.LastM3u8
            });
        },
        fetchlist() {
            listES = new EventSource("/hls/list");
            listES.onmessage = evt => {
                if (!evt.data) return;
                this.Rooms = JSON.parse(evt.data);
                this.Rooms.sort((a, b) =>
                    a.StreamPath > b.StreamPath ? 1 : -1
                );
            };
        },
        saveTs(item) {
            let req = window.ajax.get(
                "/hls/save?streamPath=" + item.TSInfo.RoomInfo.StreamPath
            );
            this.$Notice.open({
                title: "正在保存TS文件",
                desc: "关闭后停止保存",
                duration: 0,
                onClose() {
                    req.abort();
                }
            });
        }
    },
    mounted() {
        this.fetchlist();
    },
    deactivated() {
        listES.close();
    }
};
</script>

<style>
@import url("/iview.css");
.layout {
    padding-bottom: 30px;
    display: flex;
    flex-wrap: wrap;
}
.ts-info {
    width: 300px;
}

.hls-info {
    width: 350px;
    display: flex;
    flex-direction: column;
}
</style>