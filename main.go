package main

import (
	"bytes"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bogem/id3v2"

	"github.com/go-flac/flacpicture"
	"github.com/go-flac/flacvorbis"

	"github.com/gammazero/workerpool"
	"github.com/go-flac/go-flac"
)

var (
	Key = []byte{0x23, 0x31, 0x34, 0x6C, 0x6A, 0x6B, 0x5F, 0x21, 0x5C, 0x5D, 0x26, 0x30, 0x55, 0x3C, 0x27, 0x28}
)

func PKCS7UnPadding(src []byte) []byte {
	length := len(src)
	unpadding := int(src[length-1])
	return src[:(length - unpadding)]
}

func decode(meta []byte) ([]byte, error) {
	d64 := base64.NewDecoder(base64.StdEncoding, bytes.NewReader(meta))
	enc, err := ioutil.ReadAll(d64)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(Key)
	if err != nil {
		return nil, err
	}
	res := make([]byte, 0)
	for len(enc) > 0 {
		bsize := block.BlockSize()
		dst := make([]byte, bsize)
		block.Decrypt(dst, enc)
		res = append(res, dst...)
		enc = enc[bsize:]
	}
	return PKCS7UnPadding(res), nil
}

func containPNGHeader(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	return string(data[:8]) == string([]byte{137, 80, 78, 71, 13, 10, 26, 10})
}

func extractFromFLAC(fn string) (*MetaInfo, error) {
	f, err := flac.ParseFile(fn)
	if err != nil {
		return nil, err
	}
	for _, meta := range f.Meta {
		if meta.Type == flac.VorbisComment {
			cmts, err := flacvorbis.ParseFromMetaDataBlock(*meta)
			if err != nil {
				return nil, err
			}
			descs, err := cmts.Get("description")
			if err != nil {
				return nil, err
			}
			for _, desc := range descs {
				if strings.HasPrefix(desc, "163 key") {
					res, err := decode([]byte(strings.TrimPrefix(desc, "163 key(Don't modify):")))
					if err != nil {
						return nil, err
					}
					info := new(MetaInfo)
					if err := json.Unmarshal(res[6:], info); err != nil {
						return nil, err
					}
					return info, nil
				}
			}
		}
	}
	return nil, errors.New("meta not found")
}

func extractFromMp3(fn string) (*MetaInfo, error) {
	f, err := id3v2.Open(fn, id3v2.Options{Parse: true})
	defer f.Close()
	if err != nil {
		return nil, err
	}
	res := f.GetFrames("COMM")
	for _, frame := range res {
		if cmt, ok := frame.(id3v2.CommentFrame); ok {
			if strings.HasPrefix(cmt.Text, "163 key") {
				res, err := decode([]byte(strings.TrimPrefix(cmt.Text, "163 key(Don't modify):")))
				if err != nil {
					return nil, err
				}
				info := new(MetaInfo)
				if err := json.Unmarshal(res[6:], info); err != nil {
					return nil, err
				}
				return info, nil
			}
		}
	}
	return nil, errors.New("meta not found")
}

func downloadPic(url string) ([]byte, string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	resp, err := client.Get(url)
	if err != nil || resp.StatusCode != 200 {
		if err == nil {
			err = fmt.Errorf("remote returned %d", resp.StatusCode)
		}
		return []byte(url), "-->", err
	}
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return []byte(url), "-->", err
	}
	mime := "image/jpeg"
	if containPNGHeader(data) {
		mime = "image/png"
	}
	return data, mime, nil
}

func addFLACTag(fileName string, meta *MetaInfo) {
	f, err := flac.ParseFile(fileName)
	changed := false
	if err != nil {
		log.Println(err)
		return
	}
	func() {
		for _, meta := range f.Meta {
			if meta.Type == flac.Picture {
				return
			}
		}
		if meta.AlbumPic == "" {
			return
		}
		if pic, mime, err := downloadPic(meta.AlbumPic); err == nil {
			picture, err := flacpicture.NewFromImageData(flacpicture.PictureTypeFrontCover, "Front cover", pic, mime)
			if err == nil {
				changed = true
				log.Println("Adding image")
				picturemeta := picture.Marshal()
				f.Meta = append(f.Meta, &picturemeta)
			} else {
				log.Println(err)
			}
		} else {
			log.Println(err)
			if mime == "-->" {
				picture := &flacpicture.MetadataBlockPicture{
					PictureType: flacpicture.PictureTypeFrontCover,
					MIME:        "-->",
					Description: "Front cover",
					ImageData:   pic,
				}
				changed = true
				log.Println("Adding image URL")
				picturemeta := picture.Marshal()
				f.Meta = append(f.Meta, &picturemeta)
			}
		}
	}()

	var cmtmeta *flac.MetaDataBlock
	for _, m := range f.Meta {
		if m.Type == flac.VorbisComment {
			cmtmeta = m
			break
		}
	}
	var cmts *flacvorbis.MetaDataBlockVorbisComment
	if cmtmeta != nil {
		cmts, err = flacvorbis.ParseFromMetaDataBlock(*cmtmeta)
		if err != nil {
			log.Println(err)
			return
		}
	} else {
		cmts = flacvorbis.New()
	}

	if titles, err := cmts.Get(flacvorbis.FIELD_TITLE); err != nil {
		log.Println(err)
		return
	} else if len(titles) == 0 {
		if name := meta.MusicName; name != "" {
			log.Println("Adding music name")
			changed = true
			cmts.Add(flacvorbis.FIELD_TITLE, name)
		}
	}

	if albums, err := cmts.Get(flacvorbis.FIELD_ALBUM); err != nil {
		log.Println(err)
		return
	} else if len(albums) == 0 {
		if name := meta.Album; name != "" {
			log.Println("Adding album name")
			changed = true
			cmts.Add(flacvorbis.FIELD_ALBUM, name)
		}
	}

	if artists, err := cmts.Get(flacvorbis.FIELD_ARTIST); err != nil {
		log.Println(err)
		return
	} else if len(artists) == 0 {
		if artist := meta.Artist; artist != nil {
			log.Println("Adding artist")
			for _, name := range artist {
				changed = true
				cmts.Add(flacvorbis.FIELD_ARTIST, name[0].(string))
			}
		}
	}
	res := cmts.Marshal()
	if cmtmeta != nil {
		*cmtmeta = res
	} else {
		f.Meta = append(f.Meta, &res)
	}

	if changed {
		if err := f.Save(fileName); err != nil {
			log.Println(err)
		}
	}
}

func addMP3Tag(fileName string, meta *MetaInfo) {
	tag, err := id3v2.Open(fileName, id3v2.Options{Parse: true})
	changed := false
	if err != nil {
		log.Println(err)
		return
	}
	defer tag.Close()

	func() {
		if meta.AlbumPic == "" {
			return
		}
		for _, meta := range tag.AllFrames() {
			for _, frame := range meta {
				if _, ok := frame.(id3v2.PictureFrame); ok {
					return
				}
			}
		}
		if pic, mime, err := downloadPic(meta.AlbumPic); err != nil {
			log.Println(err)
			if mime == "-->" {
				changed = true
				fmt.Println("Adding image URL")
				pic := id3v2.PictureFrame{
					Encoding:    id3v2.EncodingISO,
					MimeType:    mime,
					PictureType: id3v2.PTFrontCover,
					Description: "Front cover",
					Picture:     pic,
				}
				tag.AddAttachedPicture(pic)
			}
		} else {
			changed = true
			fmt.Println("Adding image")
			pic := id3v2.PictureFrame{
				Encoding:    id3v2.EncodingISO,
				MimeType:    mime,
				PictureType: id3v2.PTFrontCover,
				Description: "Front cover",
				Picture:     pic,
			}
			tag.AddAttachedPicture(pic)
		}
	}()

	if tag.GetTextFrame("TIT2").Text == "" {
		if meta.MusicName != "" {
			log.Println("Adding music name")
			changed = true
			tag.AddTextFrame("TIT2", id3v2.EncodingUTF8, meta.MusicName)
		}
	}

	if tag.GetTextFrame("TALB").Text == "" {
		if meta.Album != "" {
			log.Println("Adding album name")
			changed = true
			tag.AddTextFrame("TALB", id3v2.EncodingUTF8, meta.Album)
		}
	}

	if tag.GetTextFrame("TPE1").Size() == 0 {
		if meta.Artist != nil {
			log.Println("Adding artist")
			for _, name := range meta.Artist {
				changed = true
				tag.AddTextFrame("TPE1", id3v2.EncodingUTF8, name[0].(string))
			}
		}
	}

	if !changed {
		return
	}

	if err = tag.Save(); err != nil {
		log.Println(err)
	}
}

// yoki123/ncmdump
type MetaInfo struct {
	MusicID       int             `json:"musicId"`
	MusicName     string          `json:"musicName"`
	Artist        [][]interface{} `json:"artist"` // [[string,int],]
	AlbumID       int             `json:"albumId"`
	Album         string          `json:"album"`
	AlbumPicDocID interface{}     `json:"albumPicDocId"` // string or int
	AlbumPic      string          `json:"albumPic"`
	BitRate       int             `json:"bitrate"`
	Mp3DocID      string          `json:"mp3DocId"`
	Duration      int             `json:"duration"`
	MvID          int             `json:"mvId"`
	Alias         []string        `json:"alias"`
	TransNames    []interface{}   `json:"transNames"`
	Format        string          `json:"format"`
}

func main() {
	argc := len(os.Args)
	if argc <= 1 {
		log.Println("please input file path!")
		return
	}
	files := make([]string, 0)

	for i := 0; i < argc-1; i++ {
		path := os.Args[i+1]
		if info, err := os.Stat(path); err != nil {
			log.Fatalf("Path %s does not exist.", info)
		} else if info.IsDir() {
			filelist, err := ioutil.ReadDir(path)
			if err != nil {
				log.Fatalf("Error while reading %s: %s", path, err.Error())
			}
			for _, f := range filelist {
				files = append(files, filepath.Join(path, "./", f.Name()))
			}
		} else {
			files = append(files, path)
		}
	}

	pool := workerpool.New(16)
	for _, fn := range files {
		filename := fn
		pool.Submit(func() {
			ext := filepath.Ext(filename)
			fmt.Println(filename)
			if _, err := os.Stat(strings.TrimSuffix(filename, ext) + ".ncm"); err == nil {
				log.Printf("Skipping %s as ncm file present\n", filename)
				return
			}
			switch filepath.Ext(filename) {
			case ".flac":
				data, err := extractFromFLAC(filename)
				if err != nil {
					log.Println(err)
					return
				}
				addFLACTag(filename, data)
			case ".mp3":
				data, err := extractFromMp3(filename)
				if err != nil {
					log.Println(err)
					return
				}
				addMP3Tag(filename, data)
			}
		})
	}
	pool.StopWait()
}
