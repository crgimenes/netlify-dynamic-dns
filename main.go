package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-openapi/runtime"
	"github.com/go-openapi/strfmt"
	"github.com/gosidekick/goconfig"
	"github.com/netlify/open-api/go/models"
	"github.com/netlify/open-api/go/plumbing/operations"
	"github.com/netlify/open-api/go/porcelain"
)

var cfg = &struct {
	AccessToken    string `cfgRequired:"true"`
	Zone           string `cfgRequired:"true"`
	Record         string `cfgDefault:"home"`
	zoneID         string `cfg:"-"`
	recordHostname string `cfg:"-"`
}{}

const maxDelay = 15

var netlify = porcelain.NewRetryable(
	porcelain.Default.Transport,
	nil,
	porcelain.DefaultRetryAttempts)

var netlifyAuth = runtime.ClientAuthInfoWriterFunc(
	func(r runtime.ClientRequest, _ strfmt.Registry) error {
		err := r.SetHeaderParam("User-Agent", "NetlifyDDNS")
		if err != nil {
			return err
		}
		err = r.SetHeaderParam("Authorization", "Bearer "+cfg.AccessToken)
		return err
	})

func GetIPv4() (string, error) {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.ipify.org?format=text", nil)
	client := &http.Client{}

	go func() {
		time.Sleep(time.Second * maxDelay)
		cancel()
	}()

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)

	return string(b), err
}

// doUpdate updates the DNS records with the public IP address.
func doUpdate() error {
	// Get the Public IP
	ipv4, err := GetIPv4()
	if err != nil {
		return fmt.Errorf("error retrieving your public ipv4 address: %w", err)
	}

	getparams := operations.NewGetDNSRecordsParams()
	getparams.ZoneID = cfg.zoneID
	resp, err := netlify.Operations.GetDNSRecords(getparams, netlifyAuth)
	if err != nil {
		return err
	}

	// Get existing records if they exist
	var existingARecord *models.DNSRecord
	for _, record := range resp.Payload {
		if record.Hostname == cfg.recordHostname {
			if record.Type == "A" {
				existingARecord = record
				break
			}
		}
	}

	if existingARecord != nil {

		if existingARecord.Hostname == cfg.recordHostname &&
			existingARecord.Value == ipv4 {
			// Current IP and registration are the same, there is no need to update.
			os.Exit(0)
		}

		log.Println(fmt.Sprintf("removing DNS record %v, ip %v",
			existingARecord.Hostname,
			existingARecord.Value,
		))

		// Delete existing records if they exist (Netlify DNS API has no update feature)
		deleteparams := operations.NewDeleteDNSRecordParams()
		deleteparams.ZoneID = existingARecord.DNSZoneID
		deleteparams.DNSRecordID = existingARecord.ID
		_, err = netlify.Operations.DeleteDNSRecord(deleteparams, netlifyAuth)
		if err != nil {
			return fmt.Errorf("error deleting existing record from Netlify DNS: %w", err)
		}
	}

	// Create new record
	ipv4Record := &models.DNSRecordCreate{
		Hostname: cfg.recordHostname,
		Type:     "A",
		Value:    ipv4,
	}
	if existingARecord != nil {
		ipv4Record.TTL = existingARecord.TTL
	}

	log.Println(fmt.Sprintf("add DNS record %v, ip %v",
		cfg.recordHostname,
		ipv4,
	))

	createparams := operations.NewCreateDNSRecordParams()
	createparams.ZoneID = cfg.zoneID
	createparams.DNSRecord = ipv4Record
	_, err = netlify.Operations.CreateDNSRecord(createparams, netlifyAuth)
	if err != nil {
		return fmt.Errorf("error creating new DNS record on Netlify DNS: %w", err)
	}

	return nil
}

func main() {
	goconfig.PrefixEnv = "netlify"

	err := goconfig.Parse(cfg)
	if err != nil {
		fmt.Println(err)
		return
	}

	cfg.zoneID = strings.ReplaceAll(cfg.Zone, ".", "_")
	cfg.recordHostname = cfg.Record + "." + cfg.Zone

	for {
		err := doUpdate()
		if err != nil {
			log.Println("error Updating DNS Record", err.Error())
			os.Exit(1)
		}
		break
	}
}
