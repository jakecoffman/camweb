package main

import (
	"encoding/base64"
	"fmt"
	"github.com/deepch/vdk/av"
	"github.com/deepch/vdk/codec/h264parser"
	"github.com/gin-gonic/gin"
	"github.com/pion/webrtc/v2"
	"github.com/pion/webrtc/v2/pkg/media"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"
)

func serveHTTP() {
	router := gin.Default()
	router.GET("/", func(context *gin.Context) {
		context.File("index.html")
	})
	router.POST("/receive", receiver)
	router.GET("/codec/:uuid", func(c *gin.Context) {
		if streams, ok := config.Streams[c.Param("uuid")]; ok {
			c.JSON(200, streams.Codecs)
		} else {
			c.JSON(404, "No codecs found")
		}
	})
	router.POST("/control/:id", func(c *gin.Context) {
		type Command struct {
			Cmd string `json:"cmd" binding:"eq=start|eq=stop"`
			Dir string `json:"dir" binding:"eq=Left|eq=Right|eq=Up|eq=Down"`
		}
		var cmd Command
		if err := c.BindJSON(&cmd); err != nil {
			c.AbortWithStatusJSON(400, "Bad request: "+err.Error())
			return
		}
		if streams, ok := config.Streams[c.Param("id")]; ok {
			url := streams.URL
			if err := control(c, url, cmd.Cmd, cmd.Dir); err != nil {
				c.AbortWithStatusJSON(500, err.Error())
				return
			}
			c.JSON(200, "OK")
		} else {
			c.JSON(404, "No codecs found")
		}
	})
	fmt.Printf("http://127.0.0.1%v\n", config.Server.HTTPPort)
	err := router.Run(config.Server.HTTPPort)
	if err != nil {
		log.Fatalln("Start HTTP Server error", err)
	}
}

func receiver(c *gin.Context) {
	type Payload struct {
		SUUID string `json:"suuid"`
		Data  string `json:"data"`
	}
	var payload Payload
	if err := c.BindJSON(&payload); err != nil {
		c.AbortWithStatusJSON(400, "JSON bind error: "+err.Error())
		return
	}
	suuid := payload.SUUID

	settings, ok := config.Streams[suuid]
	if !ok {
		c.AbortWithStatusJSON(404, "stream not found")
		return
	}

	sd, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		log.Println("DecodeString error", err)
		c.AbortWithStatusJSON(400, "failed to decode data "+err.Error())
		return
	}

	mediaEngine := webrtc.MediaEngine{}
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(sd),
	}
	err = mediaEngine.PopulateFromSDP(offer)
	if err != nil {
		log.Println("PopulateFromSDP error", err)
		c.AbortWithStatusJSON(500, "media engine error")
		return
	}

	var payloadType uint8
	for _, videoCodec := range mediaEngine.GetCodecsByKind(webrtc.RTPCodecTypeVideo) {
		if videoCodec.Name == "H264" && strings.Contains(videoCodec.SDPFmtpLine, "packetization-mode=1") {
			payloadType = videoCodec.PayloadType
			break
		}
	}
	if payloadType == 0 {
		log.Println("Remote peer does not support H264")
		c.AbortWithStatusJSON(500, "remote peer does not support h264")
		return
	}
	if payloadType != 126 {
		log.Println("Video might not work with codec", payloadType)
	}
	log.Println("Work payloadType", payloadType)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))

	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	})
	if err != nil {
		log.Println("NewPeerConnection error", err)
		c.AbortWithStatusJSON(500, "peer error")
		return
	}

	peerConnection.OnICEGatheringStateChange(func(state webrtc.ICEGathererState) {
		log.Println("OnICEGatheringStateChange", state.String())
	})
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Println("OnConnectionStateChange", state.String())
	})
	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			log.Println("OnICECandidate", candidate.Address)
		} else {
			log.Println("OnICECandidate", candidate)
		}
	})
	peerConnection.OnSignalingStateChange(func(state webrtc.SignalingState) {
		log.Println("OnSignalingStateChange", state.String())
	})

	// keep-alive
	//keepAlive := 20 * time.Second
	//timer1 := time.NewTimer(keepAlive)
	peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
		log.Println("Keep alive started")
		// Register text message handling
		d.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("Message from DataChannel '%s': '%s'\n", d.Label(), string(msg.Data))
			//timer1.Reset(keepAlive)
		})
	})

	// ADD Video Track
	videoTrack, err := peerConnection.NewTrack(payloadType, rand.Uint32(), "video", suuid+"_pion")
	if err != nil {
		log.Println("Failed to create video track", err)
		c.AbortWithStatusJSON(500, "peer error")
		return
	}
	_, err = peerConnection.AddTransceiverFromTrack(videoTrack,
		webrtc.RtpTransceiverInit{
			Direction: webrtc.RTPTransceiverDirectionSendonly,
		},
	)
	if err != nil {
		log.Println("AddTransceiverFromTrack error", err)
		c.AbortWithStatusJSON(500, "peer error")
		return
	}
	_, err = peerConnection.AddTrack(videoTrack)
	if err != nil {
		log.Println("AddTrack error", err)
		c.AbortWithStatusJSON(500, "peer error")
		return
	}

	// audio
	var audioTrack *webrtc.Track
	codecs := settings.Codecs
	if len(codecs) > 1 && (codecs[1].Type() == av.PCM_ALAW || codecs[1].Type() == av.PCM_MULAW) {
		switch codecs[1].Type() {
		case av.PCM_ALAW:
			audioTrack, err = peerConnection.NewTrack(webrtc.DefaultPayloadTypePCMA, rand.Uint32(), "audio", suuid+"audio")
		case av.PCM_MULAW:
			audioTrack, err = peerConnection.NewTrack(webrtc.DefaultPayloadTypePCMU, rand.Uint32(), "audio", suuid+"audio")
		}
		if err != nil {
			log.Println(err)
			c.AbortWithStatusJSON(500, "audio error")
			return
		}
		_, err = peerConnection.AddTransceiverFromTrack(audioTrack,
			webrtc.RtpTransceiverInit{
				Direction: webrtc.RTPTransceiverDirectionSendonly,
			},
		)
		if err != nil {
			log.Println("AddTransceiverFromTrack error", err)
			c.AbortWithStatusJSON(500, "peer error")
			return
		}
		_, err = peerConnection.AddTrack(audioTrack)
		if err != nil {
			log.Println(err)
			c.AbortWithStatusJSON(500, "peer error")
			return
		}
	}
	if err := peerConnection.SetRemoteDescription(offer); err != nil {
		log.Println("SetRemoteDescription error", err, offer.SDP)
		c.AbortWithStatusJSON(500, "peer error")
		return
	}
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		log.Println("CreateAnswer error", err)
		c.AbortWithStatusJSON(500, "peer error")
		return
	}
	if err = peerConnection.SetLocalDescription(answer); err != nil {
		log.Println("SetLocalDescription error", err)
		c.AbortWithStatusJSON(500, "peer error")
		return
	}
	peerConnection.OnICEConnectionStateChange(OnICEConnectionStateChange(peerConnection, suuid, videoTrack, audioTrack))

	_, err = c.Writer.Write([]byte(base64.StdEncoding.EncodeToString([]byte(answer.SDP))))
	if err != nil {
		log.Println("Writer SDP error", err)
		c.AbortWithStatusJSON(500, "peer error")
		return
	}
}

func OnICEConnectionStateChange(pc *webrtc.PeerConnection, id string, videoTrack, audioTrack *webrtc.Track) func(state webrtc.ICEConnectionState) {
	control := make(chan bool, 10)
	settings, ok := config.Streams[id]
	if !ok {
		return nil
	}
	codecs := settings.Codecs
	sps := codecs[0].(h264parser.CodecData).SPS()
	pps := codecs[0].(h264parser.CodecData).PPS()
	once := sync.Once{}

	return func(connectionState webrtc.ICEConnectionState) {
		log.Println("OnICEConnectionStateChange", connectionState.String())
		if connectionState != webrtc.ICEConnectionStateConnected {
			once.Do(func() {
				err := pc.Close()
				if err != nil {
					log.Println("peerConnection Close error", err)
				}
				close(control)
			})
			return
		}

		//defer timer1.Stop()
		cuuid, ch := config.connect(id)
		log.Println("start stream", id, "client", cuuid)
		defer func() {
			log.Println("stop stream", id, "client", cuuid)
			defer config.disconnect(id, cuuid)
		}()
		var Vpre time.Duration
		var start bool
		//timer1.Reset(5 * time.Second)
		for {
			select {
			//case <-timer1.C:
			//	log.Println("Keep-Alive Timer")
			//	if err := pc.Close(); err != nil {
			//		log.Println("close failed", err)
			//	}
			case <-control:
				return
			case pck := <-ch:
				//timer1.Reset(2 * time.Second)
				if pck.IsKeyFrame {
					start = true
				}
				if !start {
					continue
				}
				if pck.IsKeyFrame {
					pck.Data = append([]byte("\000\000\001"+string(sps)+"\000\000\001"+string(pps)+"\000\000\001"), pck.Data[4:]...)

				} else {
					pck.Data = pck.Data[4:]
				}
				var Vts time.Duration
				if pck.Idx == 0 && videoTrack != nil {
					if Vpre != 0 {
						Vts = pck.Time - Vpre
					}
					samples := uint32(90000 / 1000 * Vts.Milliseconds())
					err := videoTrack.WriteSample(media.Sample{Data: pck.Data, Samples: samples})
					if err != nil {
						return
					}
					Vpre = pck.Time
				} else if pck.Idx == 1 && audioTrack != nil {
					err := audioTrack.WriteSample(media.Sample{Data: pck.Data, Samples: uint32(len(pck.Data))})
					if err != nil {
						return
					}
				}
			}
		}
	}
}
