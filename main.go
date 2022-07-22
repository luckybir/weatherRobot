package main

import (
	"crypto/md5"
	"encoding/hex"
	jsoniter "github.com/json-iterator/go"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
	"io/ioutil"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type Config struct {
	WeatherConfig struct {
		RegexpFilter         bool   `yaml:"regexp_filter"`
		RegexpFilterPattern  string `yaml:"regexp_filter_pattern"`
		RegexpReplaceHtmlTag bool   `yaml:"regexp_replace_html_tag"`
	} `yaml:"weather_config"`
	BotWebHook     string `yaml:"bot_web_hook"`
	CronExpression string `yaml:"cron_expression"`
	MaxID          int64  `yaml:"max_id"`
}

type WeatherContent struct {
	Data struct {
		CardListInfo struct {
			SinceID int64 `json:"since_id"`
		} `json:"cardlistInfo"`
		Cards []struct {
			CardType int64 `json:"card_type"`
			Mblog    struct {
				//CreateAt    string `json:"create_at"`
				ID         string `json:"id"`
				Text       string `json:"text"`
				BmiddlePic string `json:"bmiddle_pic"`
				PicNum     int64  `json:"pic_num"`
				//Pics []struct{
				//	PID string `json:"pid"`
				//	URL string `json:"url"`
				//}`json:"pics"`

			} `json:"mblog"`
		} `json:"cards"`
	} `json:"data"`

	OK int64 `json:"ok"`
}

type WeworkBotContent struct {
	Msgtype string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
	Image struct {
		Base64 []byte `json:"base64"`
		MD5    string `json:"md5"`
	} `json:"image"`
}

var sugar *zap.SugaredLogger
var config Config

func main() {
	initZapLog()
	initConfig()
	initCron()
}

func initZapLog() {
	logger, _ := zap.NewDevelopment()
	defer logger.Sync()

	sugar = logger.Sugar()

}

func initConfig() {
	f, err := ioutil.ReadFile("./config.yaml")
	if err != nil {
		sugar.Errorw("read config file failure",
			"err", err.Error())
	}

	err = yaml.Unmarshal(f, &config)
	if err != nil {
		sugar.Errorw("unmarshal file failure",
			"err", err.Error())
	}

}

func initCron() {
	sugar.Infof("starting go cron...")

	c := cron.New()
	c.AddFunc(config.CronExpression, weatherBotStart)
	c.Start()
	defer c.Stop()
	select {}
}

func weatherBotStart() {
	sugar.Info("weather bot start...")
	wc := new(WeatherContent)

	getWeatherContent(wc)
	processWeatherContent(wc)
	sendWeatherContent(wc)

	updateConifigMaxID()
}

func getWeatherContent(wc *WeatherContent) {
	client := http.Client{}

	url := `https://m.weibo.cn/api/container/getIndex`

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		sugar.Errorw("get weibo Content failed",
			"err", err.Error())
	}

	query := req.URL.Query()
	query.Add("uid", "2294193132")
	query.Add("luicode", "10000011")
	query.Add("lfid", "100103type=1&q=广州天气")
	query.Add("containerid", "1076032294193132")

	req.URL.RawQuery = query.Encode()

	req.Header.Add("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/90.0.4430.212 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		sugar.Errorw("api get response failed",
			"err", err.Error())
	}

	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		sugar.Errorw("read response body failure",
			"err", err.Error())
	}

	defer resp.Body.Close()

	json := jsoniter.ConfigCompatibleWithStandardLibrary
	err = json.Unmarshal(respBody, &wc)
	if err != nil {
		sugar.Errorw("json unmarshal failure",
			"err", err.Error())
	}

}

func processWeatherContent(wc *WeatherContent) {

	var currentMaxID int64

	for i := len(wc.Data.Cards) - 1; i >= 0; i-- {

		if wc.Data.Cards[i].Mblog.ID == "" {
			continue
		}

		id, err := strconv.ParseInt(wc.Data.Cards[i].Mblog.ID, 10, 64)
		if err != nil {
			sugar.Errorw("convert id error",
				"err", err.Error(),
				"ID", wc.Data.Cards[i].Mblog.ID)
		}

		if currentMaxID < id {
			currentMaxID = id
		}

		// filter old text
		if config.MaxID >= id {
			wc.Data.Cards = append(wc.Data.Cards[:i], wc.Data.Cards[i+1:]...)
			continue
		}

		//filter by pattern
		if config.WeatherConfig.RegexpFilter == true {
			matched, err := regexp.MatchString(config.WeatherConfig.RegexpFilterPattern, wc.Data.Cards[i].Mblog.Text)
			if err != nil {
				sugar.Errorw("regexp match failure",
					"err", err.Error())
			}

			if matched == false {
				wc.Data.Cards = append(wc.Data.Cards[:i], wc.Data.Cards[i+1:]...)
				continue
			}
		}

		//replace all html tag
		if config.WeatherConfig.RegexpReplaceHtmlTag == true {
			re := regexp.MustCompile("<[^>]+>")
			wc.Data.Cards[i].Mblog.Text = re.ReplaceAllString(wc.Data.Cards[i].Mblog.Text, "")
		}
	}

	config.MaxID = currentMaxID

	//sugar.Debugf("%+v", wc)
}

func sendWeatherContent(wc *WeatherContent) {

	sugar.Infof("text counts:%v", len(wc.Data.Cards))

	if len(wc.Data.Cards) == 0 {
		return
	}

	for i := 0; i < len(wc.Data.Cards); i++ {
		//send text, because wework reject weibo pic url
		if wc.Data.Cards[i].Mblog.Text != "" {
			sendWeatherContentTextToWeworkBot(wc.Data.Cards[i].Mblog.Text)
		}

		//send pic
		if wc.Data.Cards[i].Mblog.BmiddlePic != "" {
			sendWeatherContentPicToWeworkBot(wc.Data.Cards[i].Mblog.BmiddlePic)
		}
	}

}

func sendWeatherContentTextToWeworkBot(text string) {

	bc := new(WeworkBotContent)
	bc.Msgtype = "text"
	bc.Text.Content = text

	reqBody, err := jsoniter.MarshalToString(bc)
	if err != nil {
		sugar.Errorw("marshal text failure",
			"err", err.Error())
	}

	client := http.Client{}

	url := `https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=` + config.BotWebHook

	req, _ := http.NewRequest("POST", url, strings.NewReader(reqBody))

	client.Do(req)

}

func sendWeatherContentPicToWeworkBot(picURL string) {

	client := http.Client{}

	//get PIC base64 code
	req, _ := http.NewRequest("GET", picURL, nil)

	resp, _ := client.Do(req)

	respBody, _ := ioutil.ReadAll(resp.Body)

	if respBody != nil {

		url := `https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=` + config.BotWebHook

		bc := new(WeworkBotContent)

		bc.Msgtype = "image"
		bc.Image.Base64 = respBody

		h := md5.New()
		h.Write(respBody)
		bc.Image.MD5 = hex.EncodeToString(h.Sum(nil))

		reqBody, err := jsoniter.MarshalToString(bc)
		if err != nil {
			sugar.Errorw("marshal image failure",
				"err", err.Error())
		}

		req, _ := http.NewRequest("POST", url, strings.NewReader(reqBody))

		client.Do(req)

	}
}

func updateConifigMaxID() {

	f, err := yaml.Marshal(config)
	if err != nil {
		sugar.Errorw("update config file failure",
			"err", err.Error())
	}

	ioutil.WriteFile("./config.yaml", f, 0777)
}
