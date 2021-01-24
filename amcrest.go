package camweb

import (
	"bytes"
	"context"
	"fmt"
	dac "github.com/xinsnake/go-http-digest-auth-client"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

var cache = map[string]dac.DigestRequest{}
var controlLock sync.Mutex

func control(ctx context.Context, uri, cmd, arg string) error {
	controlLock.Lock()
	defer controlLock.Unlock()

	cmdUrl := strings.Replace(uri, "rtsp", "http", 1)
	const str = "%v/cgi-bin/ptz.cgi?action=%v&channel=0&code=%v&arg1=0&arg2=5&arg3=0"
	uri = fmt.Sprintf(str, cmdUrl, cmd, arg)
	u, err := url.Parse(uri)
	if err != nil {
		log.Println(err)
		return err
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	u.User = nil

	transport := dac.NewTransport(user, pass)
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		log.Println(err)
		return err
	}
	response, err := transport.RoundTrip(req)
	if err != nil {
		log.Println(err)
		return err
	}
	_, _ = ioutil.ReadAll(response.Body)
	_ = response.Body.Close()
	//log.Println("Amcrest response", response.StatusCode, string(b))
	return nil
}

func say(ctx context.Context, uri string, voiceData bytes.Buffer) error {
	controlLock.Lock()
	defer controlLock.Unlock()

	cmdUrl := strings.Replace(uri, "rtsp", "http", 1)
	const str = "%v/cgi-bin/audio.cgi?action=postAudio&httptype=singlepart&channel=1"
	uri = fmt.Sprintf(str, cmdUrl)
	u, err := url.Parse(uri)
	if err != nil {
		log.Println(err)
		return err
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	u.User = nil

	transport := dac.NewTransport(user, pass)
	req, err := http.NewRequestWithContext(ctx, "POST", uri, &voiceData)
	if err != nil {
		log.Println(err)
		return err
	}
	req.Header["Content-Type"] = []string{"audio/G.711A"}
	//req.Header["Content-Length"] = []string{"9999999"}

	response, err := transport.RoundTrip(req)
	if err != nil {
		log.Println(err)
		return err
	}
	defer response.Body.Close()
	log.Println("Done")

	d, _ := ioutil.ReadAll(response.Body)
	fmt.Println(string(d))
	_ = response.Body.Close()
	//log.Println("Amcrest response", response.StatusCode, string(b))
	return nil
}
