package main

import (
	"flag"
	"fmt"
	"log"
	"strings"

	"gitlab.com/41f04d3bba15/obfs4webrtc/internal/config"
	"gitlab.com/41f04d3bba15/obfs4webrtc/pkg/speedtest"
	"gitlab.com/41f04d3bba15/obfs4webrtc/pkg/transport"
)

func main() {
	var Help, Verbose, SpeedTest, Gui bool
	var Mode, VkTargetServer string

	flag.BoolVar(&Help, "help", false, "-h. for help.")
	flag.BoolVar(&Verbose, "verbose", false, "-verbose. for verbose mode.")
	flag.BoolVar(&SpeedTest, "speed", false, "-speed. for speed test.")
	flag.BoolVar(&Gui, "gui", false, "Show gui web browser.")

	flag.StringVar(&Mode, "mode", "server", "-mode server/client. By default server mod if the ID of who to call is not specified.")
	flag.StringVar(&VkTargetServer, "vktargetserver", "", "-vktargetserver id9999999. To choose who we call.")

	flag.Parse()

	if Help {
		flag.Usage()
	}
	if VkTargetServer != "" {
		if !strings.HasPrefix(VkTargetServer, "id") {
			fmt.Println("Please enter a valid ID starting with 'id'")
			return
		}
		VkTargetServer = strings.TrimPrefix(VkTargetServer, "id")

		Mode = "client"
	}

	if SpeedTest {
		speedtest.CheckSpeedOokla()
	}

	cfg, err := config.LoadConfig(Mode)
	if err != nil {
		fmt.Println(err)
		err = config.RunSetupWizardVk(Mode)
		if err != nil {
			log.Fatal("Error configuration: ", err)
		}
		return
	}
	if Mode == "client" {
		if VkTargetServer == "" {
			fmt.Println("Who to call?")
			flag.Usage()
			return
		}
		transport.RunBot(cfg, VkTargetServer)
	} else {
		transport.RunBot(cfg, "")
	}

	flag.Usage()
}
