package main
// PING WITH GO
// GET LAST PLAYED SONG IF PROGRESS < 0
import (
	"fmt"
	"os/exec"
	"regexp"
	"net"
	"container/ring"
	"bytes"
	"net/url"
	"strings"
	"github.com/antchfx/xmlquery"
	"os"
	"errors"
	"strconv"
	"net/http"
	"time"
	"encoding/json"
	"io/ioutil"
	"github.com/2tvenom/golifx"
	"unsafe"
	"github.com/tarm/serial"
)

const (
	SPOTIFY_PLAYING = "https://api.spotify.com/v1/me/player/currently-playing"
	SPOTIFY_RESUME = "https://api.spotify.com/v1/me/player/play"
	SPOTIFY_PAUSE = "https://api.spotify.com/v1/me/player/pause"
	SPOTIFY_TOKEN = "https://accounts.spotify.com/api/token"
	SPOTIFY_DEVICE = "https://api.spotify.com/v1/me/player/devices"
	SPOTIFY_REFRESH = "AQB1VVU7I_YHQON5VB-j8XeAqCvbo_vYDX2sWQWUOLIzo5vtq_TBYV28LI-mNhYepXHlKTjLzau6w5JgRdTh63jR79Ed_VLiADLtMqqFMKLv7lyTNrsUbkUcV7aV7uEeGuaMGw"
	SPOTIFY_SECRET = "YjdmOTUwMTliZjAyNDE0NmI1YzFhOWNmMDliYWUxYzY6YjE5MDgwYTA0YWVhNDUzM2E3Njc3NTIzZTM2MzViYjM="
	SUNRISE_API = "https://api.sunrise-sunset.org/json?lat=52.042210&lng=4.501157&date=today&formatted=0"
	MAC = "98:09:CF:73:E2:7E"
	BLUE_MAC = "98:09:CF:73:E2:7D"
	COMPUTER = "192.168.0.99:6666"
	LOG_FILE = "/home/pi/domoServer/log/wifilog-01.kismet.netxml"
	THRESHOLD = -55
	THRESHOLDB = 0
	THRESHOLD_PRESENT = -55
	DEBOUNCE_CYCLES_PRESENT = 2
	DEBOUNCE_CYCLES_ABSENT = 6
	DEBOUNCE_CYCLES_LIGHT = 3
	MEASUREMENT_OFFSET = 1 // DEBOUNCE_CYCLES_ABSENT / 4 * 3 // 4 or absent/2-1
	SLEEP = 1
	PLAY_OFFSET = 8000
	MEASUREMENT_SIZE = DEBOUNCE_CYCLES_ABSENT + MEASUREMENT_OFFSET
)

func main() {
	var sunrise time.Time
	var sunset time.Time
	var tomorrow time.Time
	var newToken time.Time
	var times map[string]string
	var signalDbm int
	var signalDbmESP int16
	var signalDbmB int
	var lightValue int16
	var bulbs []*golifx.Bulb
	var token string
	var device string
	playback := make(map[string]interface{})
	debounces := ring.New(MEASUREMENT_OFFSET)
	light := false
	present := false
	dataFetched := false
	tokenFetched := false
	paused := false
	cmd := exec.Command("rfcomm", "connect", "4", BLUE_MAC, "19")
	connBT := &serial.Config{Name: "/dev/rfcomm0", Baud: 115200}
	serialBT, _ := serial.OpenPort(connBT)
	debouncePresent := 0
	debounceAbsent := 0

	checkRSSI := func() {
		fmt.Println(signalDbm, signalDbmESP, signalDbmB, lightValue)

		if debounceAbsent > 0 && present && ((signalDbm > THRESHOLD || signalDbmESP > THRESHOLD ) || signalDbmB >= THRESHOLDB) {
			debounces = pushBack(debounces, 0)
			debounceAbsent = 0
			fmt.Println("RESET ABSENT DEBOUNCE")
		} else if debouncePresent > 0 && !present && signalDbm <= THRESHOLD_PRESENT && signalDbmESP <= THRESHOLD_PRESENT{
			fmt.Println("RESET PRESENT DEBOUNCE")
			debouncePresent = 0
		}

		if (signalDbm > THRESHOLD_PRESENT || signalDbmESP > THRESHOLD_PRESENT) && !present {
			debouncePresent++
			if debouncePresent < DEBOUNCE_CYCLES_PRESENT {
				return
			}

			go sendToComputer("unlock\n")

			if !inTimeSpan(sunrise, sunset, time.Now().UTC()) && !light {
				light = true
				var bulbState *golifx.BulbState
				for ok := true; ok; ok = (bulbState == nil) {
					bulbState, _ = bulbs[0].GetColorState()
				}
				bulbState.Color.Brightness = 32768
				bulbs[0].SetColorState(bulbState.Color, 0)
				bulbs[0].SetPowerState(true)
			}


			if paused {
				paused = false
				offset := 0;

				debounces.Do(func(p interface{}){
					if p != nil {
						fmt.Print(",",p.(int))
					}
				})
				for i:=0;i<MEASUREMENT_OFFSET;i++ {
					if debounces.Value != nil {
						if debounces.Value.(int) >= DEBOUNCE_CYCLES_ABSENT && debounces.Value.(int) < MEASUREMENT_SIZE {
							fmt.Println("DEBOUNCE DETECTED IN RANGE:", debounces.Value.(int))
							offset = debounces.Value.(int) / 2
							debounces = debounces.Move(MEASUREMENT_OFFSET-i)
							break
						}
					}
					debounces = debounces.Next()
				}

				fmt.Println("OFFSET:", offset)
				resume(token, device, playback, offset)
			}

			fmt.Println("Present:", signalDbm, signalDbmB, time.Now().UTC())
			debouncePresent = 0
			present = true
		} else if signalDbm <= THRESHOLD && signalDbmESP <= THRESHOLD && signalDbmB < THRESHOLDB && present {
			debounceAbsent++
			if debounceAbsent < DEBOUNCE_CYCLES_ABSENT {
				return
			}

			go sendToComputer("lock\n")

			if isPlaying(token) && !paused {
				paused = true
				device = getLastUsedDevice(token)
				pause(token)
				playback["context"], playback["progress"], playback["track"] = getPlayback(token)
			}

			light = false
			bulbs[0].SetPowerState(false)

			fmt.Println("Absent", signalDbm, signalDbmB, time.Now().UTC())
			present = false
			debounceAbsent = 0
		}
	}

	cmd.Start()
	fmt.Println("Looking for bulbs")
	for ok := true; ok; ok = (len(bulbs) == 0) {
		bulbs, _ = golifx.LookupBulbs()
	}

	for {
		if(time.Now().UTC().After(tomorrow)) {
			dataFetched = false
		}

		if(time.Now().UTC().After(newToken)) {
			tokenFetched = false
		}

		if time.Now().UTC().After(sunset) && (time.Now().UTC().Add(time.Duration(-10) * time.Second)).Before(sunset) {
			present = false
		}

		if dataFetched == false {
			times = fetchTimes()
			sunset, _ = time.Parse(time.RFC3339, times["sunset"])
			sunrise, _ = time.Parse(time.RFC3339, times["sunrise"])

			tomorrow = calculateTomorrow()

			dataFetched = true
		}

		if tokenFetched == false {
			token = getSpotifyToken()

			newToken = time.Now().UTC().Add(time.Minute * 50)
			tokenFetched = true
		}

		log, err := readLog()
		if err != nil {
			fmt.Println(err)
			continue
		}

		signalDbm, err = getRSSI(log)
		if err != nil {
			fmt.Println(err)
			continue
		}

		signalDbmB = getRSSIB(&cmd)
		signalDbmESP, lightValue = getRSSIESP(serialBT)

		checkRSSI();

		time.Sleep(SLEEP * time.Second)
	}
}

func getRSSIESP(serial *serial.Port) (int16, int16) {
	buf := make([]byte, 4)
	serial.Flush()
	serial.Read(buf)

	return *(*int16)(unsafe.Pointer(&buf[0])), *(*int16)(unsafe.Pointer(&buf[2]))
}

func sendToComputer(message string) {
	if conn, err := net.Dial("tcp", COMPUTER); err == nil {
		fmt.Fprintf(conn, message)
	}
}

func getRSSIB(cmd **exec.Cmd) (int){
	var rssi int
	var err error
	var raw []byte
	reg := regexp.MustCompile("[-0-9]{1,3}$")

	for {
		if (*cmd).Process != nil && err != nil {
			go (*cmd).Wait()
			(*cmd).Process.Kill()
			*cmd = exec.Command("rfcomm", "connect", "4", BLUE_MAC, "2")
			(*cmd).Start()
			time.Sleep(SLEEP * 4 * time.Second)
		}

		raw, err = exec.Command("hcitool", "rssi", BLUE_MAC).Output()

		if err != nil {
			fmt.Println(errors.New("Not connected"))
		} else {
			break
		}
	}

	out := strings.TrimSuffix(string(raw), "\n")
	match := reg.FindStringSubmatch(out)
	rssi, _ = strconv.Atoi(match[0])

	return rssi
}

func readLog() (*xmlquery.Node, error){
	xml, _ := os.Open(LOG_FILE)
	doc, err := xmlquery.Parse(xml)

	if err != nil {
		return doc, errors.New("Failed parsing")
	} else {
		return doc, nil
	}
}

func getRSSI(log *xmlquery.Node) (int, error) {
	result := xmlquery.FindOne(log, fmt.Sprintf("/detection-run/wireless-network/wireless-client[client-mac = '%s']/snr-info/last_signal_dbm", MAC))
	if result == nil {
		return -1, errors.New("Phone not found -1")
	}
	rssi, err := strconv.Atoi(result.InnerText())

	if err != nil {
		return -1, errors.New("Phone not found")
	} else {
		return rssi, nil
	}
}

func calculateTomorrow() time.Time {
	tomorrow := (time.Now().UTC().AddDate(0, 0, 1))
	return time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, tomorrow.Location())
}

func fetchTimes() map[string]string {
	client := &http.Client{}
	r, _ := client.Get(SUNRISE_API)
	defer r.Body.Close()

	body, _ := ioutil.ReadAll(r.Body)
	raw := string(body)
	json_raw := raw[11:len(raw)-15]
	var times map[string]string
	_ = json.Unmarshal([]byte(json_raw), &times)

	fmt.Println("Fetched times")
	return times
}

func inTimeSpan(start, end, check time.Time) bool {
	return check.After(start) && check.Before(end)
}

func pause(token string) {
        client := &http.Client{}
	req, _ := http.NewRequest("PUT", SPOTIFY_PAUSE, nil)
	req.Header.Set("Authorization", "Bearer " + token)
	_, _ = client.Do(req)
}

func getLastPlayed(token string) {
        client := &http.Client{}
	req, _ := http.NewRequest("PUT", SPOTIFY_PAUSE, nil)
	req.Header.Set("Authorization", "Bearer " + token)
	_, _ = client.Do(req)
}

func resume(token string, device string, playback map[string]interface{}, progressOffset int) {
	var body []byte
	progress := playback["progress"].(float64) - (DEBOUNCE_CYCLES_ABSENT * SLEEP * 1000) - (float64(progressOffset) * SLEEP * 1000) - PLAY_OFFSET
	if progress < 0 {
		// make recursive k
		//fmt.Println("UNDERFLOW")
		//offset = progress * -1
		//track, duration := getLastPlayed(token)
		//playback["track"] = track
		//progress = duration - offset
		// fetch last song and do rest from last song
		progress = 0
	}
	if playback["context"].(string) == "" || strings.Contains(playback["context"].(string), "artist") {
//		body = []byte(fmt.Sprintf("{\"uris\": [\"" + playback["track"].(string) + "\"], \"position_ms\": %f}", progress))
		body = nil
	}else {
		fmt.Println("actual:", progress)
		body = []byte(fmt.Sprintf("{\"context_uri\": \"" + playback["context"].(string) + "\",\"offset\": {\"uri\": \"" + playback["track"].(string) +"\"},\"position_ms\": %f }", progress))
	}

	req, _ := http.NewRequest("PUT", SPOTIFY_RESUME + "?device_id=" + device, bytes.NewBuffer(body))
	//req, _ := http.NewRequest("PUT", SPOTIFY_RESUME + "?device_id=" + device, nil)
	req.Header.Set("Authorization", "Bearer " + token)
	req.Header.Set("Content-Type", "application/json")
        client := &http.Client{}
	_, _ = client.Do(req)
}

func getPlayback(token string) (context string , progress float64, track string) {
        client := &http.Client{}
	req, _ := http.NewRequest("GET", SPOTIFY_PLAYING, nil)
	req.Header.Set("Authorization", "Bearer " + token)
	var result map[string]interface{}
	r, _ := client.Do(req)
	body, _ := ioutil.ReadAll(r.Body)
	raw := string(body)
	_ = json.Unmarshal([]byte(raw), &result)

	if result["context"] != nil {
		context = result["context"].(map[string]interface{})["uri"].(string)
	}else {
		context = ""
	}

	progress = result["progress_ms"].(float64)
	fmt.Println("stopped on:", progress)
	fmt.Println("estimated :", progress - (DEBOUNCE_CYCLES_ABSENT * SLEEP * 1000) - PLAY_OFFSET)
	fmt.Println("subtracted:", (DEBOUNCE_CYCLES_ABSENT * SLEEP * 1000) + PLAY_OFFSET)
	track = result["item"].(map[string]interface{})["uri"].(string)

	return;
}

func isPlaying(token string) bool {
        client := &http.Client{}
	req, _ := http.NewRequest("GET", SPOTIFY_PLAYING, nil)
	req.Header.Set("Authorization", "Bearer " + token)
	var result map[string]interface{}
	r, _ := client.Do(req)
	body, _ := ioutil.ReadAll(r.Body)
	raw := string(body)
	_ = json.Unmarshal([]byte(raw), &result)

	if result["is_playing"] == nil {
		return false
	}
	return result["is_playing"].(bool)
}

func getLastUsedDevice(token string) string {
        client := &http.Client{}
	req, _ := http.NewRequest("GET", SPOTIFY_DEVICE, nil)
	req.Header.Set("Authorization", "Bearer " + token)
	var result map[string]interface{}
	r, _ := client.Do(req)
	body, _ := ioutil.ReadAll(r.Body)
	raw := string(body)
	_ = json.Unmarshal([]byte(raw), &result)

	i := 0;
	for {
		if result["devices"].([]interface{})[i].(map[string]interface{})["is_active"].(bool) {
			return result["devices"].([]interface{})[i].(map[string]interface{})["id"].(string)
		}
		i++;
	}

}

func getSpotifyToken() string {
        client := &http.Client{}
        values := url.Values{}
	values.Add("grant_type", "refresh_token")
	values.Add("refresh_token", SPOTIFY_REFRESH)
	values.Add("redirect_uri", "http%3A%2F%2F127.0.0.1")
	req, _ := http.NewRequest("POST", SPOTIFY_TOKEN, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic " + SPOTIFY_SECRET)
	var result map[string]interface{}
	for result["access_token"] == nil {
		fmt.Println("Trying to get token")
		r, _ := client.Do(req)
		body, _ := ioutil.ReadAll(r.Body)
		raw := string(body)
		_ = json.Unmarshal([]byte(raw), &result)
		time.Sleep(SLEEP * time.Second)
	}
	fmt.Println("Token fetched")

	return result["access_token"].(string)
}

func ra(r *ring.Ring, n int) int {
	return r.Move(n).Value.(int)
}

func push(r *ring.Ring, n int) *ring.Ring {
	r.Prev().Value = n
	return r
}

func pushBack(r *ring.Ring, n int) *ring.Ring {
	length := r.Len()
	r.Move(length).Value = n
	r = r.Move(-(length-1))
        return r
}
