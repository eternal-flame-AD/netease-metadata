package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type (
	Lyric struct {
		NoLyric bool          `json:"nolyric"`
		Lrc     *LyricElement `json:"lrc"`
		Klrc    *LyricElement `json:"klyric"`
		Tlrc    *LyricElement `json:"tlyric"`
		Code    int           `json:"code"`
	}

	LyricElement struct {
		Version int     `json:"version"`
		Lyric   *string `json:"lyric"`
	}
)

func GetLyric(id int) (*Lyric, error) {
	client := http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Get("https://music.163.com/api/song/lyric?lv=1&kv=1&tv=-1&id=" + strconv.Itoa(id))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Remote returned %d", resp.StatusCode)
	}
	dec := json.NewDecoder(resp.Body)
	res := Lyric{}
	if err := dec.Decode(&res); err != nil {
		return nil, err
	}
	return &res, nil
}
