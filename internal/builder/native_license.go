package builder

// A native package carries the model's license and notices: the license
// files and model card at the top of its Hugging Face repository, read with
// the same client the FileFreak archive uses for its license rule.

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/seanbrill/local-llm/internal/catalog"
	"github.com/seanbrill/local-llm/internal/filefreak"
)

const maxNoticeFile = 4 << 20

// newHF is the Hugging Face client; tests point it at a fake.
var newHF = filefreak.NewHF

type noticeFile struct {
	Rel  string // path in the package (licenses/model/<file>)
	Data []byte
}

// packageNotices is what a package says about the model's license.
type packageNotices struct {
	License     string // e.g. "apache-2.0"; "" = unknown
	LicenseName string // a custom license's name, from the model card
	LicenseLink string
	SourceURL   string // https://huggingface.co/<org>/<repo>
	Shareable   bool   // the license lets anybody redistribute the files
	Why         string // why it isn't shareable, or why the license is unknown
	Files       []noticeFile
}

// modelNotices reads the license and notice files of the model's repository
// (and the vision projector's, when it comes from another one). It never
// fails a package: what it can't read is said in the log and the README.
func modelNotices(ctx context.Context, m catalog.Model, log LogFunc) packageNotices {
	var n packageNotices
	hf := newHF()
	main, ok := filefreak.ParseHFURL(m.URL, hf.Base)
	if !ok {
		n.Why = "the model's file doesn't come from Hugging Face, so the factory couldn't read its license"
		log("Model license: unknown (" + n.Why + "). Check it with whoever publishes the model before you share the package.")
		return n
	}
	n.SourceURL = strings.TrimRight(filefreak.DefaultHFBase, "/") + "/" + main.Repo
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	type repo struct {
		file     filefreak.HFFile
		url, dir string
	}
	repos := []repo{{main, m.URL, "licenses/model"}}
	if m.MMProjURL != "" {
		if mm, ok := filefreak.ParseHFURL(m.MMProjURL, hf.Base); ok && mm.Repo != main.Repo {
			repos = append(repos, repo{mm, m.MMProjURL, "licenses/vision-projector"})
		}
	}
	for i, r := range repos {
		off, err := hf.Head(ctx, r.url)
		if err != nil {
			if i == 0 {
				n.Why = "Hugging Face couldn't be asked about the license (" + err.Error() + ")"
				log("Model license: unknown, " + n.Why + ". The README links to the model's page instead.")
				return n
			}
			continue
		}
		info, err := hf.Info(ctx, r.file.Repo, off.Commit)
		if err != nil {
			if i == 0 {
				n.Why = "Hugging Face's model info couldn't be read (" + err.Error() + ")"
				log("Model license: unknown, " + n.Why + ". The README links to the model's page instead.")
				return n
			}
			continue
		}
		if i == 0 {
			n.License = strings.Join(info.Licenses, ", ")
			n.LicenseName, n.LicenseLink = info.LicenseName, info.LicenseLink
			ok, _, why := filefreak.LicenseVerdict(info)
			n.Shareable, n.Why = ok, why
		}
		var got []string
		for _, f := range filefreak.NoticeFiles(info.Siblings) {
			var buf bytes.Buffer
			if err := hf.Fetch(ctx, hf.FileURL(r.file.Repo, off.Commit, f), &buf, maxNoticeFile); err != nil {
				log(fmt.Sprintf("Couldn't copy %s from %s: %v", f, r.file.Repo, err))
				continue
			}
			n.Files = append(n.Files, noticeFile{Rel: path.Join(r.dir, f), Data: buf.Bytes()})
			got = append(got, f)
		}
		if i == 0 {
			lic := n.License
			if lic == "" {
				lic = "not declared"
			}
			msg := "Model license: " + lic
			if len(got) > 0 {
				msg += " (" + strings.Join(got, ", ") + " copied into licenses/model/)"
			}
			log(msg + ".")
			if !n.Shareable && n.Why != "" {
				log("Check the model's license before you share this package: " + n.Why + ".")
			}
		}
	}
	return n
}
