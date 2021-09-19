package crypto

import (
	"crypto/rc4"
	"crypto/md5"
	"crypto/cipher"

	"github.com/v2fly/v2ray-core/v4/common"
)

func NewRc4Md5Stream(key []byte, iv []byte) cipher.Stream {
	h:= md5.New()
	h.Write(key)
	h.Write(iv)
	block, err := rc4.NewCipher(h.Sum(nil))
	common.Must(err)
	return block
}
