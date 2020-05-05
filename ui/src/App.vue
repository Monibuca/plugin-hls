<template>
    <div>
        <mu-data-table :columns="columns" :data="Streams">
            <template #expand="{row:item}">
                <div>
                    <m-button @click="showIndexM3u8(item)">📃Index</m-button>
                    <m-button @click="showAudioM3u8(item)" v-if="item.Audio.LastM3u8.length">📑Audio</m-button>
                    <m-button @click="showVideoM3u8(item)">📑Video</m-button>
                    <m-button @click="saveTs(item)">💾Save</m-button>
                </div>
            </template>
            <template #default="{row:item}">
                <td>{{item.TSInfo.StreamInfo.StreamPath}}</td>
                <td><StartTime :value="item.TSInfo.StreamInfo.StartTime"></StartTime></td>
                <td><Tooltip :content="item.TSInfo.BufferLength+'/2048'" style="width: 240px">
                        <Progress :stroke-width="20" :percent="Math.ceil(item.TSInfo.BufferLength*100/2048)"
                            text-inside />
                    </Tooltip></td>
                    <td>{{item.TSInfo.TotalPesCount}}</td>
                    <td>{{item.Audio.M3U8Count}}</td>
                    <td>{{item.Audio.TSCount}}</td>
                    <td>{{item.Video.M3U8Count}}</td>
                    <td>{{item.Video.TSCount}}</td>
            <template>
        </mu-data-table>
        <mu-dialog title="拉流转发" width="360" :open.sync="openPull">
            <mu-text-field v-model="remoteAddr" label="hls url" label-float help-text="Please enter URL of m3u8...">
            </mu-text-field>
            <mu-text-field v-model="streamPath" label="streamPath" label-float
                help-text="Please enter streamPath to publish."></mu-text-field>
            <mu-button slot="actions" flat color="primary" @click="addPull">确定</mu-button>
        </mu-dialog>
    </div>
</template>

<script>
let listES = null;
export default {
    data() {
        return {
            currentStream: null,
            Streams: [],
            remoteAddr: "",
            streamPath: "",
            openPull: false,
            columns:["StreamPath","开始时间","缓冲","PES总数","音频m3u8数","音频ts数","视频m3u8数","视频ts数"].map(title=>({title}))
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
            listES = new EventSource(this.apiHost + "/hls/list");
            listES.onmessage = evt => {
                if (!evt.data) return;
                this.Streams = JSON.parse(evt.data) || [];
                this.Streams.sort((a, b) =>
                    a.TSInfo.StreamInfo.StreamPath > b.TSInfo.StreamInfo.StreamPath
                        ? 1
                        : -1
                );
            };
        },
        saveTs(item) {
            let req = this.ajax.get(
                this.apiHost +
                    "/hls/save?streamPath=" +
                    item.TSInfo.StreamInfo.StreamPath
            );
            this.$Notice.open({
                title: "正在保存TS文件",
                desc: "关闭后停止保存",
                duration: 0,
                onClose() {
                    req.abort();
                }
            });
        },
        addPull() {
            this.openPull = false;
            this.ajax
                .getJSON(this.apiHost + "/hls/pull", {
                    target: this.remoteAddr,
                    streamPath: this.streamPath
                })
                .then(x => {
                    if (x.code == 0) {
                        this.$toast.success("已启动拉流");
                    } else {
                        this.$toast.error(x.msg);
                    }
                });
        }
    },
    mounted() {
        this.fetchlist();
        let _this = this
        this.$parent.titleOps = [
            {
                template:"<m-button @click='onClick'>拉流转发</m-button>",
                methods:{
                    onClick(){
                        _this.openPull = true;
                    }
                }
            }
        ];
    },
    destroyed() {
        listES.close();
    }
};
</script>

<style>
.empty {
    color: #ffc107;
    width: 100%;
    min-height: 500px;
    display: flex;
    justify-content: center;
    align-items: center;
}

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