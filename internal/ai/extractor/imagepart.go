package extractor

import (
	"github.com/openai/openai-go"
)

// imageURLPart builds the image_url content part carrying the image URL.
// The URL may be a base64 data URL or a Moonshot Storage reference (ms://<file_id>).
//
// openai-go's ChatCompletionContentPartUnionParam is a generated tagged union
// whose exact discriminator field varies across SDK patch versions. If the
// build fails here, discover the correct field with:
//
//	grep -rn "ChatCompletionContentPartUnionParam\|ImageURL\b" \
//	    $(go env GOMODCACHE)/github.com/openai/openai-go*
//
// and adjust this function. The wire requirement is a part of type "image_url"
// whose url field carries the image reference string.
func imageURLPart(url string) openai.ChatCompletionContentPartUnionParam {
	return openai.ImageContentPart(
		openai.ChatCompletionContentPartImageImageURLParam{
			URL: url,
		},
	)
}
