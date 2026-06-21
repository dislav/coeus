package extractor

import (
	"github.com/openai/openai-go"
)

// imageURLPart builds the image_url content part carrying the data URL.
//
// openai-go's ChatCompletionContentPartUnionParam is a generated tagged union
// whose exact discriminator field varies across SDK patch versions. If the
// build fails here, discover the correct field with:
//
//	grep -rn "ChatCompletionContentPartUnionParam\|ImageURL\b" \
//	    $(go env GOMODCACHE)/github.com/openai/openai-go*
//
// and adjust this function. The wire requirement is a part of type "image_url"
// whose url field is the data URL string.
func imageURLPart(dataURL string) openai.ChatCompletionContentPartUnionParam {
	return openai.ImageContentPart(
		openai.ChatCompletionContentPartImageImageURLParam{
			URL: dataURL,
		},
	)
}
