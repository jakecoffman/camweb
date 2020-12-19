package main

import (
	"fmt"
	dac "github.com/xinsnake/go-http-digest-auth-client"
	"io/ioutil"
	"log"
	"net/url"
	"strings"
	"sync"
)

var cache = map[string]dac.DigestRequest{}
var controlLock sync.Mutex

func control(uri, cmd, arg string) error {
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

	dr, ok := cache[u.String()]
	if ok {
		dr.UpdateRequest(user, pass, "GET", u.String(), "")
	} else {
		dr = dac.NewRequest(user, pass, "GET", u.String(), "")
		cache[u.String()] = dr
	}
	response, err := dr.Execute()
	if err != nil {
		log.Println(err)
		return err
	}
	_, _ = ioutil.ReadAll(response.Body)
	_ = response.Body.Close()
	//log.Println("Amcrest response", response.StatusCode, string(b))
	return nil
}
