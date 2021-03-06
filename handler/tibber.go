package handler

import (
	"time"

	"github.com/futurehomeno/fimpgo"
	"github.com/futurehomeno/fimpgo/edgeapp"
	log "github.com/sirupsen/logrus"
	tibber "github.com/tskaard/tibber-golang"
)

// AuthData is used to store all the tokens and expire information
type AuthData struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// TibberHandler structure
type TibberHandler struct {
	mqtt   *fimpgo.MqttTransport
	client *tibber.Client
	stream *tibber.Stream
	//streams      map[string]*tibber.Stream
	msgChan      tibber.MsgChan
	ticker       *time.Ticker
	home         *tibber.Home
	appLifecycle *edgeapp.Lifecycle
}

// NewTibberHandler construct new handler
func NewTibberHandler(transport *fimpgo.MqttTransport, appLifecycle *edgeapp.Lifecycle) *TibberHandler {
	th := &TibberHandler{
		mqtt:         transport,
		appLifecycle: appLifecycle,
		client:       tibber.NewClient(""),
		msgChan:      make(tibber.MsgChan),
		home:         &tibber.Home{},
	}
	th.stream = tibber.NewStream("", "")
	th.StartStreamStateEventListener()
	return th
}

// Start tibber handler service and listen to ws events
func (th *TibberHandler) Start(token string, homeID string) error {
	var err error
	var home tibber.Home
	th.client.Token = token
	for i := 0; i < 10; i++ {
		home, err = th.client.GetHomeById(homeID)
		if err == nil {
			break
		} else {
			log.Error("<tibber> error getting home by id")
			time.Sleep(60 * time.Second)
		}
	}
	if err != nil {
		return err
	}
	log.Info("The Home successfully fetched from Tibber.")
	th.home = &home
	// Setting up stream
	th.stream.Token = token
	th.stream.ID = th.home.ID
	th.stream.StartSubscription(th.msgChan)
	go func(msgChan tibber.MsgChan) {
		for {
			select {
			case msg := <-msgChan:
				th.routeTibberMessage(msg)
			}
		}
	}(th.msgChan)
	th.startPolling()
	return err
}

// StartStreamStateEventListener start event listener
func (th *TibberHandler) StartStreamStateEventListener() {
	go func() {
		for {
			stateMsg := <-th.stream.StateReportChan()
			switch stateMsg.State {
			case tibber.StreamStateConnected:
				th.appLifecycle.SetConnectionState(edgeapp.ConnStateConnected)
			case tibber.StreamStateDisconnected:
				th.appLifecycle.SetConnectionState(edgeapp.ConnStateDisconnected)
			}
		}
	}()
}

func (th *TibberHandler) startPolling() {
	// Set up ticker to poll information from Tibber
	var fiveMinutes = 5 * time.Minute
	th.ticker = time.NewTicker(fiveMinutes)
	go func() {
		for range th.ticker.C {
			if time.Now().Minute() >= 5 { // Run ticker only on minutes 0 - 4
				return
			}
			if th.appLifecycle.AppState() == edgeapp.AppStateRunning {
				currentPrice, err := th.client.GetCurrentPrice(th.home.ID)
				if err != nil {
					log.Error("Cannot get prices from Tibber - ", err)
					return
				}
				th.sendSensorReportMsg(th.home.ID, "sensor_price", currentPrice.Total, currentPrice.Currency, nil)
				log.Debug("sensor_price sent")
			} else {
				log.Debug("------- NOT CONNECTED -------")
			}
		}
	}()
}

func (th *TibberHandler) routeTibberMessage(msg *tibber.StreamMsg) {
	log.Debug("New tibber msg")
	if th.home.ID == msg.HomeID {
		// Chek if measurement has power reading
		// Should be enough to only send extended report, but app does not use power from extended report yet.
		// This is a "fix" for Kamstrup that only sends data every 10 sec
		if msg.Payload.Data.LiveMeasurement.HasProductionOrConsumptionPower() {
			watt := calculateSinglePowerValue(msg.Payload.Data.LiveMeasurement)
			th.sendMeterReportMsg(msg.HomeID, float64(watt), "W", nil)
		}
		// Check if this is an extended or normal report
		if msg.Payload.Data.LiveMeasurement.IsExtended() {
			th.sendMeterExtendedReportMsg(msg.HomeID, msg.Payload.Data.LiveMeasurement.AsFloatMap(), nil)
		}
	}
}

// calculateSinglePowerValue returns + or - wattage
func calculateSinglePowerValue(liveData tibber.LiveMeasurement) float64 {
	var val float64
	if liveData.Power > 0 {
		val = liveData.Power
	} else if liveData.PowerProduction > 0 {
		val = -liveData.PowerProduction
	}
	return val
}

func (th *TibberHandler) sendSensorReportMsg(addr string, service string, value float64, unit string, oldMsg *fimpgo.FimpMessage) {
	props := make(map[string]string)
	props["unit"] = unit
	msg := fimpgo.NewMessage("evt.sensor.report", service, "float", value, props, nil, oldMsg)
	adr, _ := fimpgo.NewAddressFromString("pt:j1/mt:evt/rt:dev/rn:tibber/ad:1/sv:" + service + "/ad:" + addr)
	th.mqtt.Publish(adr, msg)
}

func (th *TibberHandler) sendMeterReportMsg(addr string, value float64, unit string, oldMsg *fimpgo.FimpMessage) {
	service := "meter_elec"
	props := make(map[string]string)
	props["unit"] = unit
	msg := fimpgo.NewMessage("evt.meter.report", "meter_elec", "float", value, props, nil, oldMsg)
	adr, _ := fimpgo.NewAddressFromString("pt:j1/mt:evt/rt:dev/rn:tibber/ad:1/sv:" + service + "/ad:" + addr)
	if err := th.mqtt.Publish(adr, msg); err != nil {
		log.WithError(err).Error("Could not publish MQTT message")
	}
}

func (th *TibberHandler) sendMeterExtendedReportMsg(addr string, value map[string]float64, oldMsg *fimpgo.FimpMessage) {
	service := "meter_elec"
	msg := fimpgo.NewFloatMapMessage("evt.meter_ext.report", "meter_elec", value, nil, nil, oldMsg)
	adr, _ := fimpgo.NewAddressFromString("pt:j1/mt:evt/rt:dev/rn:tibber/ad:1/sv:" + service + "/ad:" + addr)
	if err := th.mqtt.Publish(adr, msg); err != nil {
		log.WithError(err).Error("Could not publish MQTT message")
	}
}
