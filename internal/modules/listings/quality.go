package listings

import "unicode/utf8"

// QualityInput is what the score looks at.
type QualityInput struct {
	HasLocation, HasLandmark, HasDigitalAddress bool
	HeadlineLen, DescriptionLen                 int
	Amenities                                   int
	FeesComplete                                bool
	HasAvailableFrom                            bool
	Photos                                      int
	HasVideo                                    bool
}

// Tip is advice shown next to the score, strongest first.
type Tip struct {
	Text   string
	Points int
	Step   string // wizard step that fixes it
}

// Quality returns a 0–100 score and tips (ProjectRequirement Phase 2).
// Photos carry the most weight: they're what renters look at first.
func Quality(q QualityInput) (int, []Tip) {
	score := 0
	var tips []Tip
	award := func(ok bool, pts int, tip Tip) {
		if ok {
			score += pts
			return
		}
		tip.Points = pts
		tips = append(tips, tip)
	}
	award(q.HasLocation, 10, Tip{Text: "Drop a pin so renters can find it on the map", Step: "location"})
	award(q.FeesComplete, 20, Tip{Text: "Fill in every fee — listings with the full move-in cost get more viewing requests", Step: "pricing"})
	award(q.Photos >= 8, 25, Tip{Text: photoTip(q.Photos), Step: "photos"})
	award(q.HasVideo, 5, Tip{Text: "Add a short walk-through video", Step: "photos"})
	award(q.DescriptionLen >= 200, 15, Tip{Text: "Write a few lines about the room, the compound and the area (200+ characters)", Step: "details"})
	award(q.HeadlineLen >= 20, 5, Tip{Text: "Give it a clear headline, like “Self-contained chamber & hall near KNUST gate”", Step: "details"})
	award(q.Amenities >= 3, 10, Tip{Text: "Tick the amenities you offer — renters filter by them", Step: "amenities"})
	award(q.HasLandmark, 5, Tip{Text: "Add a landmark (“behind the Ayigya Zongo mosque”) — it helps on viewing day", Step: "location"})
	award(q.HasDigitalAddress, 3, Tip{Text: "Add the GhanaPostGPS digital address", Step: "location"})
	award(q.HasAvailableFrom, 2, Tip{Text: "Say when it's available from", Step: "details"})

	// Strongest tips first; stable for equal points.
	for i := 1; i < len(tips); i++ {
		for j := i; j > 0 && tips[j].Points > tips[j-1].Points; j-- {
			tips[j], tips[j-1] = tips[j-1], tips[j]
		}
	}
	return score, tips
}

func photoTip(n int) string {
	switch {
	case n == 0:
		return "Add at least 8 photos — listings with photos get far more views"
	case n == 7:
		return "Add 1 more photo to get 2× more views"
	case n < 8:
		return "Add " + itoa(8-n) + " more photos to get 2× more views"
	}
	return ""
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }
