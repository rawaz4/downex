package scene

import (
	"downex/internal/filecheck"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
)

// The file check of docs/DESKTOP-SPEC.md §9 in the simulated daemon: the fixture token
// "check" gives scene downloads the results downexd reports, with the texts it reports
// (internal/filecheck), for the demo and the fit and render tests:
//   - drone-footage-coast.mp4 (completed): DANGER, a Windows program named as a video;
//   - podcast-episode-41.mp3 (completed): CAUTION, a web page instead of the file;
//   - ambient-loop-3.mp3 (completed): NOTE, an MPEG-4 audio file named as an MP3;
//   - os-image-26.04-amd64.iso (downloading): an EARLY DANGER, a Windows program;
//   - invoice.pdf.exe: a browser hand-off awaiting confirmation, the NAME stage's DANGER.

// FixtureCheckNames are the downloads the "check" token marks, in the order above.
var FixtureCheckNames = []string{"drone-footage-coast.mp4", "podcast-episode-41.mp3", "ambient-loop-3.mp3",
	"os-image-26.04-amd64.iso", "invoice.pdf.exe"}

// protoCheck renders a result as downexd does (daemon protoFileCheck).
func protoCheck(fc *model.FileCheck, name string) *downexv1.FileCheck {
	if fc == nil {
		return nil
	}
	return &downexv1.FileCheck{
		Level: downexv1.FileCheckLevel(fc.Level), Stage: downexv1.FileCheckStage(fc.Stage + 1), Reason: fc.Reason,
		Content: fc.Content, ContentLabel: filecheck.ContentLabel(fc), ClaimLabel: filecheck.ClaimLabel(fc),
		ServedType: fc.Served, Member: fc.Member, ProgramMembers: int32(fc.ProgramMembers),
		ListingIncomplete: fc.ListingIncomplete, Summary: filecheck.Summary(fc, name), Detail: filecheck.Detail(fc),
	}
}

// applyCheckFixture marks the scene's downloads for the "check" token.
func (m *Mock) applyCheckFixture() error {
	byName := map[string]*dl{}
	for _, s := range m.list() {
		byName[s.d.filename] = s.d
	}
	results := map[string]*model.FileCheck{
		"drone-footage-coast.mp4": {Level: model.FileCheckDanger, Stage: model.FileCheckStageFinal, Reason: "program_disguised",
			Content: "pe", Claim: "mp4", Served: "video/mp4"},
		"podcast-episode-41.mp3": {Level: model.FileCheckCaution, Stage: model.FileCheckStageFinal, Reason: "web_page",
			Content: "html", Claim: "mp3", Served: "text/html"},
		"ambient-loop-3.mp3": {Level: model.FileCheckNote, Stage: model.FileCheckStageFinal, Reason: "other_type",
			Content: "isobmff", Brand: "audio", Claim: "mp3"},
		"os-image-26.04-amd64.iso": {Level: model.FileCheckDanger, Stage: model.FileCheckStageEarly, Reason: "program_disguised",
			Content: "pe", Claim: "iso"},
	}
	for name, fc := range results {
		d := byName[name]
		if d == nil {
			return fixtureError(`no download "` + name + `" to check`)
		}
		d.fileCheck = protoCheck(fc, name)
	}
	add, err := m.addNow(&downexv1.AddRequest{Url: "https://files.example.com/get/invoice.pdf.exe",
		Headers: []*downexv1.Header{{Name: "Referer", Value: "https://files.example.com/"}}, Filename: "invoice.pdf.exe",
		Source: srcBrowser, ExpectedSize: 2_400_000, Mime: "application/pdf"})
	if err != nil {
		return err
	}
	if s := m.sims[add.GetId()]; s != nil && s.d.state == state.StateAwaiting {
		s.d.fileCheck = protoCheck(filecheck.CheckName(s.d.filename, false, s.d.mime), s.d.filename)
	}
	return nil
}
