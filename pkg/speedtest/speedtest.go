package speedtest

import (
	"fmt"
	"log"

	"github.com/showwin/speedtest-go/speedtest"
)

// go get github.com/showwin/speedtest-go
func CheckSpeedOokla() {
	serverList, _ := speedtest.FetchServers()
	targets, _ := serverList.FindServer([]int{})

	fmt.Println("Speed ​​testing has begun.")

	for _, s := range targets {
		// Please make sure your host can access this test server,
		// otherwise you will get an error.
		// It is recommended to replace a server at this time
		checkError(s.PingTest(nil))
		checkError(s.DownloadTest())
		checkError(s.UploadTest())

		// Note: The unit of s.DLSpeed, s.ULSpeed is bytes per second, this is a float64.
		fmt.Printf("Latency: %s, Download: %s, Upload: %s\n", s.Latency, s.DLSpeed, s.ULSpeed)
		s.Context.Reset()
	}
}

func checkError(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
