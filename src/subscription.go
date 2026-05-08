package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type SubscriptionResponse struct {
	NodeID       int    `json:"node_id"`
	Name         string `json:"name"`
	Server       string `json:"server"`
	ServerPort   int    `json:"server_port"`
	Type         string `json:"type"`
	Password     string `json:"password"`
	EdgePSK      string `json:"aero_v2_edge_psk"`
	AEADKey      string `json:"aero_v2_aead_key"`
	ResumeTicket string `json:"aero_v2_resume_ticket,omitempty"`
}

type SubscriptionInfo struct {
	Upload   int64
	Download int64
	Total    int64
	Expire   int64
}

func fetchSubscription(rawURL string) ([]AeroNode, error) {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{},
		Renegotiation:      tls.RenegotiateFreelyAsClient,
	}

	transport := &http.Transport{
		TLSClientConfig:     tlsConfig,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:         1,
		IdleConnTimeout:      30 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}

	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	info := parseSubInfo(resp.Header)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var rawNodes []SubscriptionResponse
	if err := json.Unmarshal(body, &rawNodes); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	nodes := make([]AeroNode, 0, len(rawNodes))
	for _, rn := range rawNodes {
		if rn.Type != "aero_v2" {
			continue
		}
		nodes = append(nodes, AeroNode{
			ID:       rn.NodeID,
			Server:   rn.Server,
			Port:     rn.ServerPort,
			Password: rn.Password,
			EdgePSK:  rn.EdgePSK,
			AEADKey:  rn.AEADKey,
			Name:     rn.Name,
		})
	}

	if info != nil && info.Total > 0 {
		fmt.Printf("Used: %.2f GB / %.2f GB | Expires: %s\n",
			float64(info.Upload+info.Download)/(1<<30),
			float64(info.Total)/(1<<30),
			time.Unix(info.Expire, 0).Format("2006-01-02"),
		)
	}

	fmt.Printf("Found %d aero_v2 nodes\n", len(nodes))
	return nodes, nil
}

func parseSubInfo(header http.Header) *SubscriptionInfo {
	raw := header.Get("Subscription-Userinfo")
	if raw == "" {
		return nil
	}
	info := &SubscriptionInfo{}
	fmt.Sscanf(raw, "upload=%d; download=%d; total=%d; expire=%d",
		&info.Upload, &info.Download, &info.Total, &info.Expire)
	return info
}
