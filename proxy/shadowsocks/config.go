package shadowsocks

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/sha1"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"github.com/v2fly/v2ray-core/v4/common"
	"github.com/v2fly/v2ray-core/v4/common/antireplay"
	"github.com/v2fly/v2ray-core/v4/common/buf"
	"github.com/v2fly/v2ray-core/v4/common/crypto"
	"github.com/v2fly/v2ray-core/v4/common/protocol"
)

// MemoryAccount is an account type converted from Account.
type MemoryAccount struct {
	Cipher Cipher
	Key    []byte

	replayFilter antireplay.GeneralizedReplayFilter
}

// Equals implements protocol.Account.Equals().
func (a *MemoryAccount) Equals(another protocol.Account) bool {
	if account, ok := another.(*MemoryAccount); ok {
		return bytes.Equal(a.Key, account.Key)
	}
	return false
}

func (a *MemoryAccount) CheckIV(iv []byte) error {
	if a.replayFilter == nil {
		return nil
	}
	if a.replayFilter.Check(iv) {
		return nil
	}
	return newError("IV is not unique")
}

func createAesGcm(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	common.Must(err)
	gcm, err := cipher.NewGCM(block)
	common.Must(err)
	return gcm
}

func createChaCha20Poly1305(key []byte) cipher.AEAD {
	ChaChaPoly1305, err := chacha20poly1305.New(key)
	common.Must(err)
	return ChaChaPoly1305
}

func (a *Account) getCipher() (Cipher, error) {
	switch a.CipherType {
	case CipherType_AES_128_GCM:
		return &AEADCipher{
			KeyBytes:        16,
			IVBytes:         16,
			AEADAuthCreator: createAesGcm,
		}, nil
	case CipherType_AES_256_GCM:
		return &AEADCipher{
			KeyBytes:        32,
			IVBytes:         32,
			AEADAuthCreator: createAesGcm,
		}, nil
	case CipherType_CHACHA20_POLY1305:
		return &AEADCipher{
			KeyBytes:        32,
			IVBytes:         32,
			AEADAuthCreator: createChaCha20Poly1305,
		}, nil
	case CipherType_NONE:
		return NoneCipher{}, nil
	case CipherType_AES_128_CFB:
		return &AesCfb{KeyBytes: 16}, nil
	case CipherType_AES_256_CFB:
		return &AesCfb{KeyBytes: 32}, nil
	case CipherType_CHACHA20:
		return &ChaCha20{IVBytes: 8}, nil
	case CipherType_CHACHA20_IETF:
		return &ChaCha20{IVBytes: 12}, nil
	case CipherType_AES_128_CTR:
		return &AesCtr{KeyBytes: 16}, nil
	case CipherType_AES_256_CTR:
		return &AesCtr{KeyBytes: 32}, nil
	case CipherType_RC4_MD5:
		return &Rc4Md5{IVBytes: 16}, nil
	default:
		return nil, newError("Unsupported cipher.")
	}
}

// AsAccount implements protocol.AsAccount.
func (a *Account) AsAccount() (protocol.Account, error) {
	Cipher, err := a.getCipher()
	if err != nil {
		return nil, newError("failed to get cipher").Base(err)
	}
	return &MemoryAccount{
		Cipher: Cipher,
		Key:    passwordToCipherKey([]byte(a.Password), Cipher.KeySize()),
		replayFilter: func() antireplay.GeneralizedReplayFilter {
			if a.IvCheck {
				return antireplay.NewBloomRing()
			}
			return nil
		}(),
	}, nil
}

// Cipher is an interface for all Shadowsocks ciphers.
type Cipher interface {
	KeySize() int32
	IVSize() int32
	NewEncryptionWriter(key []byte, iv []byte, writer io.Writer) (buf.Writer, error)
	NewDecryptionReader(key []byte, iv []byte, reader io.Reader) (buf.Reader, error)
	IsAEAD() bool
	EncodePacket(key []byte, b *buf.Buffer) error
	DecodePacket(key []byte, b *buf.Buffer) error
}

type ChaCha20 struct {
	IVBytes int32
}

func (*ChaCha20) IsAEAD() bool {
	return false
}

func (v *ChaCha20) KeySize() int32 {
	return 32
}

func (v *ChaCha20) IVSize() int32 {
	return v.IVBytes
}

func (v *ChaCha20) NewEncryptionWriter(key []byte, iv []byte, writer io.Writer) (buf.Writer, error) {
	stream := crypto.NewChaCha20Stream(key, iv)
	return &buf.SequentialWriter{Writer: crypto.NewCryptionWriter(stream, writer)}, nil
}

func (v *ChaCha20) NewDecryptionReader(key []byte, iv []byte, reader io.Reader) (buf.Reader, error) {
	stream := crypto.NewChaCha20Stream(key, iv)
	return &buf.SingleReader{Reader: crypto.NewCryptionReader(stream, reader)}, nil
}

func (v *ChaCha20) EncodePacket(key []byte, b *buf.Buffer) error {
	iv := b.BytesTo(v.IVSize())
	stream := crypto.NewChaCha20Stream(key, iv)
	stream.XORKeyStream(b.BytesFrom(v.IVSize()), b.BytesFrom(v.IVSize()))
	return nil
}

func (v *ChaCha20) DecodePacket(key []byte, b *buf.Buffer) error {
	if b.Len() <= v.IVSize() {
		return newError("insufficient data: ", b.Len())
	}
	iv := b.BytesTo(v.IVSize())
	stream := crypto.NewChaCha20Stream(key, iv)
	stream.XORKeyStream(b.BytesFrom(v.IVSize()), b.BytesFrom(v.IVSize()))
	b.Advance(v.IVSize())
	return nil
}

type AesCfb struct {
	KeyBytes int32
}

func (*AesCfb) IsAEAD() bool {
	return false
}

func (v *AesCfb) KeySize() int32 {
	return v.KeyBytes
}

func (v *AesCfb) IVSize() int32 {
	return 16
}

func (v *AesCfb) NewEncryptionWriter(key []byte, iv []byte, writer io.Writer) (buf.Writer, error) {
	stream := crypto.NewAesEncryptionStream(key, iv)
	return &buf.SequentialWriter{Writer: crypto.NewCryptionWriter(stream, writer)}, nil
}

func (v *AesCfb) NewDecryptionReader(key []byte, iv []byte, reader io.Reader) (buf.Reader, error) {
	stream := crypto.NewAesDecryptionStream(key, iv)
	return &buf.SingleReader{Reader: crypto.NewCryptionReader(stream, reader)}, nil
}

func (v *AesCfb) EncodePacket(key []byte, b *buf.Buffer) error {
	iv := b.BytesTo(v.IVSize())
	stream := crypto.NewAesEncryptionStream(key, iv)
	stream.XORKeyStream(b.BytesFrom(v.IVSize()), b.BytesFrom(v.IVSize()))
	return nil
}

func (v *AesCfb) DecodePacket(key []byte, b *buf.Buffer) error {
	if b.Len() <= v.IVSize() {
		return newError("insufficient data: ", b.Len())
	}
	iv := b.BytesTo(v.IVSize())
	stream := crypto.NewAesDecryptionStream(key, iv)
	stream.XORKeyStream(b.BytesFrom(v.IVSize()), b.BytesFrom(v.IVSize()))
	b.Advance(v.IVSize())
	return nil
}

type AesCtr struct {
	KeyBytes int32
}

func (*AesCtr) IsAEAD() bool {
	return false
}

func (v *AesCtr) KeySize() int32 {
	return v.KeyBytes
}

func (v *AesCtr) IVSize() int32 {
	return 16
}

func (v *AesCtr) NewEncryptionWriter(key []byte, iv []byte, writer io.Writer) (buf.Writer, error) {
	stream := crypto.NewAesCTRStream(key, iv)
	return &buf.SequentialWriter{Writer: crypto.NewCryptionWriter(stream, writer)}, nil
}

func (v *AesCtr) NewDecryptionReader(key []byte, iv []byte, reader io.Reader) (buf.Reader, error) {
	stream := crypto.NewAesCTRStream(key, iv)
	return &buf.SingleReader{Reader: crypto.NewCryptionReader(stream, reader)}, nil
}

func (v *AesCtr) EncodePacket(key []byte, b *buf.Buffer) error {
	iv := b.BytesTo(v.IVSize())
	stream := crypto.NewAesCTRStream(key, iv)
	stream.XORKeyStream(b.BytesFrom(v.IVSize()), b.BytesFrom(v.IVSize()))
	return nil
}

func (v *AesCtr) DecodePacket(key []byte, b *buf.Buffer) error {
	if b.Len() <= v.IVSize() {
		return newError("insufficient data: ", b.Len())
	}
	iv := b.BytesTo(v.IVSize())
	stream := crypto.NewAesCTRStream(key, iv)
	stream.XORKeyStream(b.BytesFrom(v.IVSize()), b.BytesFrom(v.IVSize()))
	b.Advance(v.IVSize())
	return nil
}

type Rc4Md5 struct {
	IVBytes int32
}

func (*Rc4Md5) IsAEAD() bool {
	return false
}

func (v *Rc4Md5) KeySize() int32 {
	return 16
}

func (v *Rc4Md5) IVSize() int32 {
	return v.IVBytes
}

func (v *Rc4Md5) NewEncryptionWriter(key []byte, iv []byte, writer io.Writer) (buf.Writer, error) {
	stream := crypto.NewRc4Md5Stream(key, iv)
	return &buf.SequentialWriter{Writer: crypto.NewCryptionWriter(stream, writer)}, nil
}

func (v *Rc4Md5) NewDecryptionReader(key []byte, iv []byte, reader io.Reader) (buf.Reader, error) {
	stream := crypto.NewRc4Md5Stream(key, iv)
	return &buf.SingleReader{Reader: crypto.NewCryptionReader(stream, reader)}, nil
}

func (v *Rc4Md5) EncodePacket(key []byte, b *buf.Buffer) error {
	iv := b.BytesTo(v.IVSize())
	stream := crypto.NewRc4Md5Stream(key, iv)
	stream.XORKeyStream(b.BytesFrom(v.IVSize()), b.BytesFrom(v.IVSize()))
	return nil
}

func (v *Rc4Md5) DecodePacket(key []byte, b *buf.Buffer) error {
	if b.Len() <= v.IVSize() {
		return newError("insufficient data: ", b.Len())
	}
	iv := b.BytesTo(v.IVSize())
	stream := crypto.NewRc4Md5Stream(key, iv)
	stream.XORKeyStream(b.BytesFrom(v.IVSize()), b.BytesFrom(v.IVSize()))
	b.Advance(v.IVSize())
        return nil
}

type AEADCipher struct {
	KeyBytes        int32
	IVBytes         int32
	AEADAuthCreator func(key []byte) cipher.AEAD
}

func (*AEADCipher) IsAEAD() bool {
	return true
}

func (c *AEADCipher) KeySize() int32 {
	return c.KeyBytes
}

func (c *AEADCipher) IVSize() int32 {
	return c.IVBytes
}

func (c *AEADCipher) createAuthenticator(key []byte, iv []byte) *crypto.AEADAuthenticator {
	nonce := crypto.GenerateInitialAEADNonce()
	subkey := make([]byte, c.KeyBytes)
	hkdfSHA1(key, iv, subkey)
	return &crypto.AEADAuthenticator{
		AEAD:           c.AEADAuthCreator(subkey),
		NonceGenerator: nonce,
	}
}

func (c *AEADCipher) NewEncryptionWriter(key []byte, iv []byte, writer io.Writer) (buf.Writer, error) {
	auth := c.createAuthenticator(key, iv)
	return crypto.NewAuthenticationWriter(auth, &crypto.AEADChunkSizeParser{
		Auth: auth,
	}, writer, protocol.TransferTypeStream, nil), nil
}

func (c *AEADCipher) NewDecryptionReader(key []byte, iv []byte, reader io.Reader) (buf.Reader, error) {
	auth := c.createAuthenticator(key, iv)
	return crypto.NewAuthenticationReader(auth, &crypto.AEADChunkSizeParser{
		Auth: auth,
	}, reader, protocol.TransferTypeStream, nil), nil
}

func (c *AEADCipher) EncodePacket(key []byte, b *buf.Buffer) error {
	ivLen := c.IVSize()
	payloadLen := b.Len()
	auth := c.createAuthenticator(key, b.BytesTo(ivLen))

	b.Extend(int32(auth.Overhead()))
	_, err := auth.Seal(b.BytesTo(ivLen), b.BytesRange(ivLen, payloadLen))
	return err
}

func (c *AEADCipher) DecodePacket(key []byte, b *buf.Buffer) error {
	if b.Len() <= c.IVSize() {
		return newError("insufficient data: ", b.Len())
	}
	ivLen := c.IVSize()
	payloadLen := b.Len()
	auth := c.createAuthenticator(key, b.BytesTo(ivLen))

	bbb, err := auth.Open(b.BytesTo(ivLen), b.BytesRange(ivLen, payloadLen))
	if err != nil {
		return err
	}
	b.Resize(ivLen, int32(len(bbb)))
	return nil
}

type NoneCipher struct{}

func (NoneCipher) KeySize() int32 { return 0 }
func (NoneCipher) IVSize() int32  { return 0 }
func (NoneCipher) IsAEAD() bool {
	return false
}

func (NoneCipher) NewDecryptionReader(key []byte, iv []byte, reader io.Reader) (buf.Reader, error) {
	return buf.NewReader(reader), nil
}

func (NoneCipher) NewEncryptionWriter(key []byte, iv []byte, writer io.Writer) (buf.Writer, error) {
	return buf.NewWriter(writer), nil
}

func (NoneCipher) EncodePacket(key []byte, b *buf.Buffer) error {
	return nil
}

func (NoneCipher) DecodePacket(key []byte, b *buf.Buffer) error {
	return nil
}

func passwordToCipherKey(password []byte, keySize int32) []byte {
	key := make([]byte, 0, keySize)

	md5Sum := md5.Sum(password)
	key = append(key, md5Sum[:]...)

	for int32(len(key)) < keySize {
		md5Hash := md5.New()
		common.Must2(md5Hash.Write(md5Sum[:]))
		common.Must2(md5Hash.Write(password))
		md5Hash.Sum(md5Sum[:0])

		key = append(key, md5Sum[:]...)
	}
	return key
}

func hkdfSHA1(secret, salt, outKey []byte) {
	r := hkdf.New(sha1.New, secret, salt, []byte("ss-subkey"))
	common.Must2(io.ReadFull(r, outKey))
}
