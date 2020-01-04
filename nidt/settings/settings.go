package settings

import (
	"encoding/json"
	"io/ioutil"
	"os"
)

type Setting struct {
	Addr1      string `json:"addr1"`
	Addr2      string `json:"addr2"`
	Addr3      string `json:"addr3"`
	Buffersize int    `json:"buffersize"`
	Expire     int64  `json:"expire"`
}

var mSetting Setting

func Init(path string) error {

	fi, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fi.Close()

	fd, err := ioutil.ReadAll(fi)
	if err != nil {
		return err
	}

	err = json.Unmarshal(fd, &mSetting)
	if err != nil {
		return err
	}

	return nil
}

func unInit() {
	mSetting = Setting{}
}
func GetAddr1() string {
	return mSetting.Addr1
}

func GetAddr2() string {
	return mSetting.Addr2
}

func GetAddr3() string {
	return mSetting.Addr3
}

func GetBufferSize() int {
	return mSetting.Buffersize
}

func GetExpire() int64 {
	return mSetting.Expire
}
