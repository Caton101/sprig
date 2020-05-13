package main

import (
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"gioui.org/app"
	"gioui.org/font/gofont"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"git.sr.ht/~whereswaldon/forest-go"
	forestArch "git.sr.ht/~whereswaldon/forest-go/archive"
	"git.sr.ht/~whereswaldon/forest-go/fields"
	"git.sr.ht/~whereswaldon/sprout-go"
	"git.sr.ht/~whereswaldon/wisteria/archive"

	"golang.org/x/exp/shiny/materialdesign/icons"
)

func main() {
	gofont.Register()
	go func() {
		w := app.NewWindow()
		if err := eventLoop(w); err != nil {
			log.Println(err)
			return
		}
	}()
	app.Main()
}

func eventLoop(w *app.Window) error {
	address := flag.String("address", "", "arbor relay address to connect to")
	flag.Parse()
	appState, err := NewAppState()
	if err != nil {
		return err
	}
	appState.SubscribableStore.SubscribeToNewMessages(func(n forest.Node) {
		w.Invalidate()
	})
	appState.Settings.Address = *address
	appState.UIState.ConnectFormState.Editor.SetText(*address)
	appState.UIState.FirstFrame = true
	gtx := new(layout.Context)
	for {
		switch event := (<-w.Events()).(type) {
		case system.DestroyEvent:
			return event.Err
		case system.FrameEvent:
			gtx.Reset(event.Queue, event.Config, event.Size)
			Layout(appState, gtx)
			event.Frame(gtx.Ops)
		}
	}
}

type AppState struct {
	Settings
	ArborState
	UIState
	*material.Theme
}

func NewAppState() (*AppState, error) {
	memStore := forest.NewMemoryStore()
	arch, err := archive.NewArchive(memStore)
	if err != nil {
		return nil, fmt.Errorf("failed to build archive: %w", err)
	}
	forestArch := forestArch.New(arch)
	store := sprout.NewSubscriberStore(arch)
	return &AppState{
		ArborState: ArborState{
			SubscribableStore: store,
			Archive:           arch,
			ForestArchive:     forestArch,
		},
		Theme: material.NewTheme(),
	}, nil
}

func (appState *AppState) Update(gtx *layout.Context) {
	appState.UIState.Update(&appState.Settings, &appState.ArborState, gtx)
}

type ArborState struct {
	sync.Once
	sprout.SubscribableStore
	*archive.Archive
	ForestArchive *forestArch.Archive

	communities []*forest.Community
	replies     []*forest.Reply

	workerLock sync.Mutex
	workerDone chan struct{}
	workerLog  *log.Logger
}

func LaunchWorker() (chan<- struct{}, *archive.Archive, sprout.SubscribableStore, error) {
	arch, err := archive.NewArchive(forest.NewMemoryStore())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed wrapping store in archive: %w", err)
	}
	store := sprout.NewSubscriberStore(arch)
	const address = "arbor.chat:7117"
	doneChan := make(chan struct{})
	sprout.LaunchSupervisedWorker(doneChan, address, store, nil, log.New(log.Writer(), address+" ", log.LstdFlags))
	return doneChan, arch, store, nil
}

func (a *ArborState) init() {
	a.Once.Do(func() {
		a.SubscribableStore.SubscribeToNewMessages(func(node forest.Node) {
			switch concreteNode := node.(type) {
			case *forest.Community:
				index := sort.Search(len(a.communities), func(i int) bool {
					return a.communities[i].ID().Equals(concreteNode.ID())
				})
				if index >= len(a.communities) {
					a.communities = append(a.communities, concreteNode)
					sort.SliceStable(a.communities, func(i, j int) bool {
						return strings.Compare(string(a.communities[i].Name.Blob), string(a.communities[j].Name.Blob)) < 0
					})
				}
			case *forest.Reply:
				a.Archive.Sort()
			}
		})
	})
}

func (a *ArborState) RestartWorker(address string) {
	a.init()
	a.workerLock.Lock()
	defer a.workerLock.Unlock()
	if a.workerDone != nil {
		close(a.workerDone)
	}
	a.workerDone = make(chan struct{})
	a.workerLog = log.New(log.Writer(), "worker "+address, log.LstdFlags|log.Lshortfile)
	go sprout.LaunchSupervisedWorker(a.workerDone, address, a.SubscribableStore, nil, a.workerLog)
}

type Settings struct {
	Address string
}

type ViewID int

const (
	ConnectForm ViewID = iota
	CommunityMenu
	ReplyView
)

type UIState struct {
	FirstFrame  bool
	CurrentView ViewID
	ConnectFormState
	CommunityMenuState
	ReplyViewState
}

func (ui *UIState) Update(config *Settings, arborState *ArborState, gtx *layout.Context) {
	switch ui.CurrentView {
	case ConnectForm:
		switch {
		case ui.ConnectFormState.ConnectButton.Clicked(gtx):
			config.Address = ui.ConnectFormState.Editor.Text()
			fallthrough
		case ui.FirstFrame && config.Address != "":
			arborState.RestartWorker(config.Address)
			ui.CurrentView = CommunityMenu
		}
	case CommunityMenu:
		if ui.CommunityMenuState.BackButton.Clicked(gtx) {
			ui.CurrentView = ConnectForm
		}
		for i := range ui.CommunityMenuState.CommunityBoxes {
			box := &ui.CommunityMenuState.CommunityBoxes[i]
			if box.Update(gtx) {
				log.Println("updated")
			}
		}
		if ui.CommunityMenuState.ViewButton.Clicked(gtx) {
			ui.CurrentView = ReplyView
		}
	case ReplyView:
		for i := range ui.ReplyViewState.ReplyStates {
			clickHandler := &ui.ReplyViewState.ReplyStates[i]
			if clickHandler.Clicked(gtx) {
				log.Printf("clicked %s", clickHandler.Reply)
				ui.ReplyViewState.Selected = clickHandler.Reply
				ui.ReplyViewState.Ancestry, _ = arborState.ForestArchive.AncestryOf(clickHandler.Reply)
				ui.ReplyViewState.Descendants, _ = arborState.ForestArchive.DescendantsOf(clickHandler.Reply)
			}
		}
	}
	ui.FirstFrame = false
}

type ConnectFormState struct {
	widget.Editor
	ConnectButton widget.Clickable
}

type CommunityMenuState struct {
	BackButton     widget.Clickable
	CommunityList  layout.List
	CommunityBoxes []widget.Bool
	ViewButton     widget.Clickable
}

type ReplyViewState struct {
	BackButton  widget.Clickable
	ReplyList   layout.List
	ReplyStates []ReplyState
	Selected    *fields.QualifiedHash
	Ancestry    []*fields.QualifiedHash
	Descendants []*fields.QualifiedHash
}

func Layout(appState *AppState, gtx *layout.Context) {
	appState.Update(gtx)
	ui := &appState.UIState
	switch ui.CurrentView {
	case ConnectForm:
		LayoutConnectForm(appState, gtx)
	case CommunityMenu:
		LayoutCommunityMenu(appState, gtx)
	case ReplyView:
		LayoutReplyView(appState, gtx)
	default:
	}
}

func LayoutConnectForm(appState *AppState, gtx *layout.Context) {
	ui := &appState.UIState
	theme := appState.Theme
	layout.Center.Layout(gtx, func() {
		layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func() {
				layout.Center.Layout(gtx, func() {
					layout.UniformInset(unit.Dp(4)).Layout(gtx, func() {
						material.Body1(theme, "Arbor Relay Address:").Layout(gtx)
					})
				})
			}),
			layout.Rigid(func() {
				layout.Center.Layout(gtx, func() {
					layout.UniformInset(unit.Dp(4)).Layout(gtx, func() {
						material.Editor(theme, "HOST:PORT").Layout(gtx, &(ui.Editor))
					})
				})
			}),
			layout.Rigid(func() {
				layout.Center.Layout(gtx, func() {
					layout.UniformInset(unit.Dp(4)).Layout(gtx, func() {
						material.Button(theme, "Connect").Layout(gtx, &(ui.ConnectButton))
					})
				})
			}),
		)
	})
}

var BackIcon *widget.Icon = func() *widget.Icon {
	icon, _ := widget.NewIcon(icons.NavigationArrowBack)
	return icon
}()

func LayoutCommunityMenu(appState *AppState, gtx *layout.Context) {
	ui := &appState.UIState
	ui.CommunityList.Axis = layout.Vertical
	theme := appState.Theme
	layout.NW.Layout(gtx, func() {
		layout.UniformInset(unit.Dp(4)).Layout(gtx, func() {
			material.IconButton(theme, BackIcon).Layout(gtx, &ui.CommunityMenuState.BackButton)
		})
	})
	width := gtx.Constraints.Width.Constrain(gtx.Px(unit.Dp(200)))
	layout.Center.Layout(gtx, func() {
		gtx.Constraints.Width.Max = width
		layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func() {
				gtx.Constraints.Width.Max = width
				layout.UniformInset(unit.Dp(4)).Layout(gtx, func() {
					material.Body1(theme, "Choose communities to join:").Layout(gtx)
				})
			}),
			layout.Rigid(func() {
				gtx.Constraints.Width.Max = width
				newCommunities := len(appState.communities) - len(ui.CommunityMenuState.CommunityBoxes)
				for ; newCommunities > 0; newCommunities-- {
					ui.CommunityMenuState.CommunityBoxes = append(ui.CommunityMenuState.CommunityBoxes, widget.Bool{})
				}
				ui.CommunityMenuState.CommunityList.Layout(gtx, len(appState.communities), func(index int) {
					gtx.Constraints.Width.Max = width
					community := appState.communities[index]
					checkbox := &ui.CommunityMenuState.CommunityBoxes[index]
					layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func() {
							layout.Flex{}.Layout(gtx,
								layout.Rigid(func() {
									layout.UniformInset(unit.Dp(8)).Layout(gtx, func() {
										box := material.CheckBox(theme, "")
										box.Layout(gtx, checkbox)
									})
								}),
								layout.Rigid(func() {
									layout.UniformInset(unit.Dp(8)).Layout(gtx, func() {
										material.H6(theme, string(community.Name.Blob)).Layout(gtx)
									})
								}),
							)
						}),
						layout.Rigid(func() {
							layout.UniformInset(unit.Dp(8)).Layout(gtx, func() {
								material.Body2(theme, community.ID().String()).Layout(gtx)
							})
						}),
					)
				})
			}),
			layout.Rigid(func() {
				gtx.Constraints.Width.Max = width
				layout.Center.Layout(gtx, func() {
					gtx.Constraints.Width.Max = width
					material.Button(theme, "View These Communities").Layout(gtx, &ui.CommunityMenuState.ViewButton)
				})
			}),
		)
	})
}

func LayoutReplyView(appState *AppState, gtx *layout.Context) {
	gtx.Constraints.Height.Min = gtx.Constraints.Height.Max
	gtx.Constraints.Width.Min = gtx.Constraints.Width.Max

	appState.ReplyViewState.ReplyList.Axis = layout.Vertical
	stateIndex := 0
	appState.ReplyViewState.ReplyList.Layout(gtx, len(appState.ArborState.Archive.ReplyList), func(index int) {
		if stateIndex >= len(appState.ReplyViewState.ReplyStates) {
			appState.ReplyViewState.ReplyStates = append(appState.ReplyViewState.ReplyStates, ReplyState{})
		}
		state := &appState.ReplyViewState.ReplyStates[stateIndex]
		reply := appState.ArborState.Archive.ReplyList[index]
		authorNode, found, err := appState.ArborState.SubscribableStore.GetIdentity(&reply.Author)
		if err != nil || !found {
			log.Printf("failed finding author %s for node %s", &reply.Author, reply.ID())
		}
		author := authorNode.(*forest.Identity)
		layout.Stack{}.Layout(gtx,
			layout.Stacked(func() {
				gtx.Constraints.Width.Min = gtx.Constraints.Width.Max
				leftInset := unit.Dp(8)
				if appState.ReplyViewState.Selected != nil && reply.ID().Equals(appState.ReplyViewState.Selected) {
					leftInset = unit.Dp(16)
				} else {
					found := false
					for _, id := range appState.ReplyViewState.Ancestry {
						if id.Equals(reply.ID()) {
							leftInset = unit.Dp(12)
							found = true
							break
						}
					}
					if !found {
						for _, id := range appState.ReplyViewState.Descendants {
							if id.Equals(reply.ID()) {
								leftInset = unit.Dp(20)
								break
							}
						}
					}
				}
				layout.Inset{Left: leftInset}.Layout(gtx, func() {
					layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func() {
							layout.NW.Layout(gtx, func() {
								material.Body2(appState.Theme, string(author.Name.Blob)).Layout(gtx)
							})
							layout.NE.Layout(gtx, func() {
								material.Body2(appState.Theme, reply.Created.Time().Local().Format("2006/01/02 15:04")).Layout(gtx)
							})
						}),
						layout.Rigid(func() {
							material.Body1(appState.Theme, string(reply.Content.Blob)).Layout(gtx)
						}),
					)
				})
			}),
			layout.Expanded(func() {
				state.Clickable.Layout(gtx)
				state.Reply = reply.ID()
			}),
		)
		stateIndex++
	})
}

type ReplyState struct {
	widget.Clickable
	Reply *fields.QualifiedHash
}