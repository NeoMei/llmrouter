package helper

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

const DomesticImageRequestKey = "domestic_image_request"

type DomesticImageRequest struct {
	Model string `json:"model"`
	Input struct {
		Messages []DomesticImageMessage `json:"messages"`
	} `json:"input"`
	Parameters struct {
		N            uint   `json:"n"`
		Size         string `json:"size,omitempty"`
		Seed         *int64 `json:"seed,omitempty"`
		Watermark    *bool  `json:"watermark,omitempty"`
		PromptExtend *bool  `json:"prompt_extend,omitempty"`
	} `json:"parameters"`
}
type DomesticImageMessage struct {
	Role    string              `json:"role"`
	Content []map[string]string `json:"content"`
}

// ValidateDomesticImage builds the only authorized native body before money.
// Remote references are not fetched: their size/codec cannot be certified.
func ValidateDomesticImage(c *gin.Context, info *relaycommon.RelayInfo, channelType int, model, billingModel string) error {
	request, ok := info.Request.(*dto.ImageRequest)
	if !ok || channelType != constant.ChannelTypeAli {
		return nil
	}
	if model != "qwen-image-2.0" && model != "qwen-image-2.0-pro" {
		return fmt.Errorf("Ali images support synchronous qwen-image-2.0 and qwen-image-2.0-pro only")
	}
	if info.RelayMode != relayconstant.RelayModeImagesGenerations && info.RelayMode != relayconstant.RelayModeImagesEdits {
		return fmt.Errorf("unsupported Ali image operation")
	}
	price, exists := ratio_setting.GetModelPrice(billingModel, false)
	if !exists || price < 0 || math.IsNaN(price) || math.IsInf(price, 0) || billing_setting.GetBillingMode(billingModel) == billing_setting.BillingModeTieredExpr {
		return fmt.Errorf("Ali images require explicit configured per-image price")
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return err
	}
	if storage.Size() > 31<<20 {
		return fmt.Errorf("image request exceeds local 31 MiB limit")
	}
	raw, err := storage.Bytes()
	if err != nil {
		return err
	}
	fields := map[string]any{}
	refs := []string{}
	if strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		if info.RelayMode != relayconstant.RelayModeImagesEdits {
			return fmt.Errorf("multipart is supported only for image editing")
		}
		_, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil {
			return err
		}
		reader := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("invalid image multipart")
			}
			name := part.FormName()
			data, err := io.ReadAll(io.LimitReader(part, MaxInlineMediaBytes+1))
			part.Close()
			if err != nil || len(data) > MaxInlineMediaBytes {
				return fmt.Errorf("reference image exceeds 10 MiB")
			}
			if part.FileName() != "" {
				if name != "image" && name != "image[]" {
					return fmt.Errorf("unsupported image file field")
				}
				if len(refs) >= 3 {
					return fmt.Errorf("image editing permits one to three references")
				}
				carrier := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
				fact, err := DecodeMediaFact("image", carrier)
				if err != nil || (fact.Format != "png" && fact.Format != "jpeg") {
					return fmt.Errorf("reference must be valid bounded PNG/JPEG")
				}
				if fact.Format == "jpeg" {
					carrier = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
				}
				refs = append(refs, carrier)
				continue
			}
			if _, exists := fields[name]; exists {
				return fmt.Errorf("duplicate image field")
			}
			switch name {
			case "n", "seed":
				v, err := strconv.ParseInt(string(data), 10, 64)
				if err != nil {
					return fmt.Errorf("invalid image integer")
				}
				fields[name] = json.Number(strconv.FormatInt(v, 10))
			case "watermark", "prompt_extend", "stream":
				v, err := strconv.ParseBool(string(data))
				if err != nil {
					return fmt.Errorf("invalid image boolean")
				}
				fields[name] = v
			default:
				fields[name] = string(data)
			}
		}
	} else {
		if len(raw) > 64<<10 {
			return fmt.Errorf("image JSON exceeds local 64 KiB limit")
		}
		if err := common.UnmarshalUnique(raw, &fields); err != nil {
			return err
		}
	}
	for name := range fields {
		switch name {
		case "model", "prompt", "n", "size", "response_format", "watermark", "seed", "prompt_extend", "stream":
		default:
			return fmt.Errorf("unsupported image field %q", name)
		}
	}
	if info.RelayMode == relayconstant.RelayModeImagesEdits && len(refs) == 0 {
		return fmt.Errorf("image editing requires one to three uploaded PNG/JPEG references; remote references are uncertified")
	}
	// 1300 UTF-8 bytes is a deliberately conservative gateway limit, not a
	// claim to reproduce the provider's 1300-token tokenizer.
	if !utf8.ValidString(request.Prompt) || len(request.Prompt) == 0 || len(request.Prompt) > 1300 {
		return fmt.Errorf("image prompt exceeds local 1300 UTF-8 byte limit or is empty")
	}
	native := DomesticImageRequest{Model: model}
	native.Parameters.N = 1
	if v, exists := fields["n"]; exists {
		number, ok := v.(json.Number)
		n, err := number.Int64()
		if !ok || err != nil || n < 1 || n > 6 || n > dto.MaxImageN {
			return fmt.Errorf("image n must be between 1 and 6")
		}
		native.Parameters.N = uint(n)
	}
	if request.Size != "" {
		w, h, ok := strings.Cut(request.Size, "x")
		width, e1 := strconv.ParseInt(w, 10, 32)
		height, e2 := strconv.ParseInt(h, 10, 32)
		if !ok || e1 != nil || e2 != nil || width <= 0 || height <= 0 || width*height < 512*512 || width*height > 2048*2048 {
			return fmt.Errorf("image size requires 262144 to 4194304 pixels")
		}
		native.Parameters.Size = w + "*" + h
	}
	if request.ResponseFormat != "" && request.ResponseFormat != "url" {
		return fmt.Errorf("Ali image response_format supports url only")
	}
	if stream, exists := fields["stream"]; exists {
		b, ok := stream.(bool)
		if !ok || b {
			return fmt.Errorf("Ali images are synchronous non-streaming")
		}
	}
	for _, name := range []string{"watermark", "prompt_extend"} {
		if v, exists := fields[name]; exists {
			b, ok := v.(bool)
			if !ok {
				return fmt.Errorf("invalid image boolean")
			}
			if name == "watermark" {
				native.Parameters.Watermark = &b
			} else {
				native.Parameters.PromptExtend = &b
			}
		}
	}
	if v, exists := fields["seed"]; exists {
		number, ok := v.(json.Number)
		seed, err := number.Int64()
		if !ok || err != nil || seed < 0 || seed > 2147483647 {
			return fmt.Errorf("image seed must be 0 to 2147483647")
		}
		native.Parameters.Seed = common.GetPointer(int64(seed))
	}
	content := make([]map[string]string, 0, len(refs)+1)
	for _, ref := range refs {
		content = append(content, map[string]string{"image": ref})
	}
	content = append(content, map[string]string{"text": request.Prompt})
	native.Input.Messages = []DomesticImageMessage{{Role: "user", Content: content}}
	c.Set(DomesticImageRequestKey, native)
	return nil
}

func ValidateNativeImagePrice(c *gin.Context, info *relaycommon.RelayInfo) error {
	if c.GetInt("channel_type") != constant.ChannelTypeAli {
		return nil
	}
	if _, ok := info.Request.(*dto.ImageRequest); !ok {
		return nil
	}
	if !info.PriceData.UsePrice || info.TieredBillingSnapshot != nil {
		return fmt.Errorf("native images require per-image price")
	}
	if info.PriceData.ModelPrice > 0 && info.PriceData.GroupRatioInfo.GroupRatio > 0 && common.QuotaFromFloat(info.PriceData.ModelPrice*common.QuotaPerUnit*info.PriceData.GroupRatioInfo.GroupRatio) == 0 {
		return fmt.Errorf("native image price is below quota precision")
	}
	return nil
}
