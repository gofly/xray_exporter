package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func executeQueryStats(addr, server string, inboundDownlink, inboundUplink,
	outboundDownlink, outboundUplink, outboundDelay *prometheus.GaugeVec) error {
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
		io.Copy(io.Discard, resp.Body)
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
			Outbound map[string]struct {
				Downlink float64 `json:"downlink"`
				Uplink   float64 `json:"uplink"`
			}
		} `json:"stats"`
	}{}
	err = json.NewDecoder(resp.Body).Decode(&data)
	if err != nil {
		return err
	}
	for tag, stats := range data.Stats.Inbound {
		inboundDownlink.WithLabelValues(tag, server).Set(stats.Downlink)
		inboundUplink.WithLabelValues(tag, server).Set(stats.Uplink)
	}
	for tag, stats := range data.Stats.Outbound {
		outboundDownlink.WithLabelValues(tag, server).Set(stats.Downlink)
		outboundUplink.WithLabelValues(tag, server).Set(stats.Uplink)
	}
	for _, ob := range data.Observatory {
		if ob.Delay > 10000 {
			ob.Delay = -500
		}
		outboundDelay.WithLabelValues(ob.OutboundTag, server).Set(ob.Delay)
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
	inboundDownlink := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "xray",
		Subsystem: "traffic",
		Name:      "inbound_downlink_bytes_total",
		Help:      "downlink traffic of inbound",
	}, []string{"tag", "server"})
	inboundUplink := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "xray",
		Subsystem: "traffic",
		Name:      "inbound_uplink_bytes_total",
		Help:      "uplink traffic of inbound",
	}, []string{"tag", "server"})
	outboundDownlink := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "xray",
		Subsystem: "traffic",
		Name:      "outbound_downlink_bytes_total",
		Help:      "downlink traffic of outbound",
	}, []string{"tag", "server"})
	outboundUplink := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "xray",
		Subsystem: "traffic",
		Name:      "outbound_uplink_bytes_total",
		Help:      "uplink traffic of outbound",
	}, []string{"tag", "server"})
	outboundDelay := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "xray",
		Subsystem: "observatory",
		Name:      "outbound_delay_millisecond",
		Help:      "observatory result of outbound, the unit is millisecond",
	}, []string{"tag", "server"})
	up := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "xray",
		Subsystem: "server",
		Name:      "up",
		Help:      "xray server up state",
	}, []string{"server"})
	prometheus.MustRegister(inboundDownlink, inboundUplink,
		outboundDownlink, outboundUplink, outboundDelay, up)

	handler := promhttp.Handler()
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		inboundDownlink.Reset()
		inboundUplink.Reset()
		outboundDownlink.Reset()
		outboundUplink.Reset()
		outboundDelay.Reset()
		for _, instance := range config.Instances {
			err := executeQueryStats(instance.Host, instance.Server,
				inboundDownlink, inboundUplink, outboundDownlink, outboundUplink, outboundDelay)
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
