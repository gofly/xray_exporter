package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// import(	statsService "github.com/xtls/xray-core/app/stats/command"
// 	"google.golang.org/grpc"
// 	"google.golang.org/grpc/credentials/insecure"
// )

// func dialAPIServer(apiServerAddr string) (conn *grpc.ClientConn, ctx context.Context, close func(), err error) {
// 	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
// 	conn, err = grpc.DialContext(ctx, apiServerAddr, grpc.WithTransportCredentials(
// 		insecure.NewCredentials(),
// 	), grpc.WithBlock())
// 	if err != nil {
// 		err = fmt.Errorf("failed to dial %s", apiServerAddr)
// 	}
// 	close = func() {
// 		cancel()
// 		conn.Close()
// 	}
// 	return
// }

// func executeQueryStats(apiServerAddr, server string, down, up *prometheus.GaugeVec) error {
// 	conn, ctx, close, err := dialAPIServer(apiServerAddr)
// 	if err != nil {
// 		return err
// 	}
// 	defer close()
// 	client := statsService.NewStatsServiceClient(conn)
// 	r := &statsService.QueryStatsRequest{
// 		Pattern: "inbound>>>",
// 		Reset_:  false,
// 	}
// 	resp, err := client.QueryStats(ctx, r)
// 	if err != nil {
// 		return err
// 	}
// 	for _, stat := range resp.Stat {
// 		parts := strings.Split(stat.Name, ">>>")
// 		if len(parts) != 4 || parts[1] == "api" {
// 			continue
// 		}
// 		switch parts[3] {
// 		case "downlink":
// 			down.WithLabelValues(parts[1], server).Set(float64(stat.Value))
// 		case "uplink":
// 			up.WithLabelValues(parts[1], server).Set(float64(stat.Value))
// 		}
// 	}
// 	return nil
// }

func executeQueryStats(addr, server string, downlink, uplink, obser *prometheus.GaugeVec) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/debug/vars", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	defer func() {
		io.Copy(ioutil.Discard, resp.Body)
		resp.Body.Close()
	}()

	data := struct {
		Observatory map[string]struct {
			Delay       float64 `json:"delay"`
			OutboundTag string  `json:"outbound_tag"`
		} `json:"observatory"`
		Stats struct {
			Inbound map[string]struct {
				Downlink float64 `json:"downlink"`
				Uplink   float64 `json:"uplink"`
			} `json:"inbound"`
		} `json:"stats"`
	}{}
	err = json.NewDecoder(resp.Body).Decode(&data)
	if err != nil {
		return err
	}
	for _, tag := range []string{"tproxy", "shadowsocks", "vmess0", "vmess1"} {
		if stats, ok := data.Stats.Inbound[tag]; ok {
			downlink.WithLabelValues(tag, server).Set(stats.Downlink)
			uplink.WithLabelValues(tag, server).Set(stats.Uplink)
		}
	}
	for _, ob := range data.Observatory {
		if ob.Delay > 10000 {
			ob.Delay = -1
		}
		obser.WithLabelValues(ob.OutboundTag, server).Set(ob.Delay)
	}
	return nil
}

type Config struct {
	ListenAddr string `json:"listen_addr"`
	Instances  []struct {
		Server string `json:"server"`
		Host   string `json:"host"`
	} `json:"instances"`
}

func LoadConfig(confPath string) (*Config, error) {
	f, err := os.Open(confPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	config := &Config{}
	err = json.NewDecoder(f).Decode(config)
	return config, err
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <config file path>", os.Args[0])
		os.Exit(1)
	}
	config, err := LoadConfig(os.Args[1])
	if err != nil {
		log.Fatalf("load config with fatal: %s", err)
	}
	downlink := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "xray",
		Subsystem: "traffic",
		Name:      "downlink_bytes_total",
		Help:      "downlink traffic of inbound",
	}, []string{"inbound", "server"})
	uplink := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "xray",
		Subsystem: "traffic",
		Name:      "uplink_bytes_total",
		Help:      "uplink traffic of inbound",
	}, []string{"inbound", "server"})
	obser := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "xray",
		Subsystem: "observatory",
		Name:      "delay_millisecond",
		Help:      "observatory result of outbound, the unit is millisecond",
	}, []string{"outbound", "server"})
	up := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "xray",
		Subsystem: "server",
		Name:      "up",
		Help:      "xray server up state",
	}, []string{"server"})
	prometheus.MustRegister(downlink, uplink, obser, up)

	handler := promhttp.Handler()
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		for _, instance := range config.Instances {
			err := executeQueryStats(instance.Host, instance.Server, downlink, uplink, obser)
			if err != nil {
				log.Println(err)
				up.WithLabelValues(instance.Server).Set(0)
			} else {
				up.WithLabelValues(instance.Server).Set(1)
			}
		}
		handler.ServeHTTP(w, r)
	})
	log.Fatal(http.ListenAndServe(config.ListenAddr, nil))
}
