package bot

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jonas747/dca"
)

var BotToken string
var store Store

type Command string

// [SoundName]
type SoundList map[string]*Sound

// [UserID]
type Entrances map[string]*Sound

type Context struct {
	GuildID         string                   `json:"guildId"`
	UserID          string                   `json:"userId"`
	ChannelID       string                   `json:"channelId"`
	SoundsChannelID string                   `json:"soundsChannelId"`
	State           *State                   `json:"state"`
	Session         *discordgo.Session       `json:"-"`
	UserMessage     *discordgo.MessageCreate `json:"-"`
}
type Sound struct {
	MessageID string `json:"messageId"`
	URL       string `json:"url"`
	Volume    int    `json:"volume"`
	// dca uses 0-256 for some reason, try mapping it to 0-100 for better UX // change this to uint8
}

type Channels struct {
	VoiceChannels []VoiceChannel `json:"voiceChannels"`
}

type State struct {
	SoundList SoundList `json:"soundList"`
	Entrances Entrances `json:"entrances"`
	Channels  Channels  `json:"channels"`
}

// [guildID]
type Store map[string]State

type VoiceChannel struct {
	ID             string
	GuildID        string
	Name           string
	UsersConnected []discordgo.User
}

var (
	stopPlayback  = make(chan bool)
	playbackMutex sync.Mutex
)

const (
	SoundsChannel   string = "sounds"
	CommandsChannel string = "bot-commands"
)

const (
	PlaySound   Command = ",s"
	SkipSound   Command = ",ss"
	Connect     Command = ",connect"
	Help        Command = ",help"
	List        Command = ",list"
	Rename      Command = ",rename"
	AddEntrance Command = ",addentrance"
	Adjustvol   Command = ",adjustvol"
	Find        Command = ",f"
)

func Run() {
	log.SetFlags(log.LstdFlags | log.Llongfile)
	lvl := new(slog.LevelVar)

	ss := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:     lvl,
		AddSource: true,
	}))
	slog.SetDefault(ss)

	lvl.Set(slog.LevelDebug)

	discord, err := discordgo.New("Bot " + BotToken)
	if err != nil {
		panic(err)
	}

	discord.AddHandler(readyHandler)
	discord.AddHandler(voiceStateUpdate)
	discord.AddHandler(messageHandler)

	// open session
	err = discord.Open()
	if err != nil {
		panic(err)
	}

	defer discord.Close()

	// keep bot running untill there is NO os interruption (ctrl + C)
	fmt.Println("Bot is running")
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

// TODO:
// 	look into error handling in general (remove panics and handle gracefully)
// 	look into discord ui for messages (commands and message components)
//  	https://discord.com/developers/docs/interactions/overview
// 	try improving zip upload
//  keep .help up to date
// 	maybe let commands be used in sounds channel but still delete them
//  cleanup code repetition
//  improve rate limit optimization (apply commands locally, queue api calls(???))
// 	check if sound exists on upload
// 	order .list
//  when setting an entrance when use already has one, happened that the old one wasn't removed, can't reproduce
//  profile mem with max load

func getContext(d *discordgo.Session, userMsg *discordgo.MessageCreate, v *discordgo.VoiceStateUpdate) (*Context, error) {
	var userID string
	var channelID string
	var guildID string

	if userMsg != nil {
		userID = userMsg.Author.ID
		channelID = userMsg.ChannelID
		guildID = userMsg.GuildID
	} else {
		userID = v.UserID
		channelID = v.ChannelID
		guildID = v.GuildID
	}

	guildState, ok := store[guildID]
	if !ok {
		return &Context{}, errors.New("guild context not initialized")
	}

	soundsChannelID, err := getSoundsChannelID(d, guildID)
	if err != nil {
		panic(err)
	}

	ctx := &Context{
		UserID:          userID,
		ChannelID:       channelID,
		GuildID:         guildID,
		State:           &guildState,
		Session:         d,
		UserMessage:     userMsg,
		SoundsChannelID: soundsChannelID,
	}

	return ctx, nil
}

func messageHandler(d *discordgo.Session, userMsg *discordgo.MessageCreate) {
	if userMsg.Author.Bot {
		return
	}

	ctx, err := getContext(d, userMsg, nil)
	if err != nil {
		d.ChannelMessageSend(userMsg.ChannelID, "error getting context")
	}

	// for debugging, delete eventually
	if userMsg.Content == ".reset" && userMsg.Author.ID == "123475794503794690" {
		//delete all messages in channel
		messages, err := ctx.Session.ChannelMessages(ctx.ChannelID, 100, "", "", "")
		if err != nil {
			log.Fatalf("Error getting messages: %v", err)
		}

		for _, message := range messages {
			ctx.Session.ChannelMessageDelete(ctx.ChannelID, message.ID)
		}
	}

	channel, err := d.Channel(userMsg.ChannelID)
	if err != nil {
		log.Fatalf("Error getting channel: %v", err)
	}

	if channel.Name == string(SoundsChannel) {
		handleSoundsChannel(ctx)
	}

	if channel.Name == string(CommandsChannel) {
		handleCommandsChannel(ctx)
	}
}

// modified sample from github.com/jonas747/dca
func PlayAudioFile(v *discordgo.VoiceConnection, sound *Sound) {
	playbackMutex.Lock()
	defer playbackMutex.Unlock()

	err := v.Speaking(true)
	if err != nil {
		log.Fatal("Failed setting speaking", err)
	}

	opts := dca.StdEncodeOptions
	opts.RawOutput = true
	opts.Bitrate = 32
	opts.CompressionLevel = 5

	// use brain and redo this
	if sound.Volume == 0 {
		sound.Volume = 256
	} else {
		opts.Volume = sound.Volume
	}

	encodeSession, err := dca.EncodeFile(sound.URL, opts)
	if err != nil {
		log.Fatal("Failed creating an encoding session: ", err)
	}

	done := make(chan error)
	stream := dca.NewStream(encodeSession, v, done)

	for {
		select {
		case err := <-done:
			if err == dca.ErrVoiceConnClosed {
				encodeSession.Cleanup()
			} else {
				if err != nil && err != io.EOF {
					log.Fatal("An error occurred", err)
				}
			}

			v.Speaking(false)
			encodeSession.Cleanup()
			return
		case <-stopPlayback:
			// pause and wait to destroy
			// if I don't, skipping kind of distorts the sound a bit
			stream.SetPaused(true)
			v.Speaking(false)
			time.Sleep(150 * time.Millisecond)
			encodeSession.Cleanup()
			return
		}
	}
}

func tryConnectingToVoice(d *discordgo.Session, guildID string, userID string, channelID string) (*discordgo.VoiceConnection, error) {
	if userID == "" && channelID == "" {
		return nil, errors.New("specify either userID or channelID")
	}

	if channelID == "" {
		voiceState, err := d.State.VoiceState(guildID, userID)
		if err != nil {
			if err.Error() != "state cache not found" {
				return nil, err
			} else {
				return nil, nil
			}
		}
		channelID = voiceState.ChannelID
	}

	voice, err := d.ChannelVoiceJoin(guildID, channelID, false, false)
	if err != nil {
		return nil, err
	}

	return voice, nil
}

func downloadFile(filepath string, url string) (err error) {
	out, err := os.Create("sounds/" + filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

func handleSoundsChannel(ctx *Context) {
	if len(ctx.UserMessage.Attachments) > 0 {
		for _, attachment := range ctx.UserMessage.Attachments {
			if strings.Split(attachment.Filename, ".")[1] == "zip" {
				handleZipUpload(ctx, attachment)
			}

			if strings.Split(attachment.Filename, ".")[1] == "mp3" {
				ctx.State.SoundList[strings.TrimSuffix(attachment.Filename, ".mp3")] = &Sound{
					MessageID: ctx.UserMessage.ID,
					URL:       attachment.URL,
				}
			}
		}
	} else {
		m, err := ctx.Session.ChannelMessageSendReply(ctx.ChannelID, "Please use this channel for files only", ctx.UserMessage.Reference())
		if err != nil {
			log.Fatalf("Error sending message: %v", err)
		}
		time.Sleep(3 * time.Second)
		ctx.Session.ChannelMessageDelete(ctx.ChannelID, m.ID)
		ctx.Session.ChannelMessageDelete(ctx.ChannelID, ctx.UserMessage.ID)
	}
}

func handleCommandsChannel(ctx *Context) {
	uMsg := ctx.UserMessage
	if len(uMsg.Attachments) > 0 {
		ctx.Session.ChannelMessageSendReply(ctx.ChannelID, "If you're tring to upload a sound, put it in the 'sounds' channel", ctx.UserMessage.Reference())
	}

	// skips the current sound only if the message is exactly ".ss" to avoid accidental skips
	if uMsg.Content == string(SkipSound) {
		stopPlayback <- true
		return
	}

	command := strings.Split(uMsg.Content, " ")[0]
	switch {
	case command == string(Help):
		formattedMessage :=
			"### To add sounds, just send them to the 'sounds' channel as a message (just the file, no text)\n" +
				"**Commands:**\n" +
				"`,s <sound-name>` Plays a sound\n" +
				"`,connect` Connects to the voice channel you are in.\n" +
				"`,list` Lists all sounds in the sounds channel.\n" +
				"`,ss` Stops the current sound.\n" +
				"`,rename <current-name> <new-name>` Renames a sound.\n" +
				"`,addentrance <sound-name>` Sets a sound as your entrance sound.\n" +
				"`,adjustvol <sound-name> <volume>` Adjusts the volume of a sound (0-512).\n" +
				"`,f <sound-name>` Finds a sound by name and returns a link to it."

		ctx.Session.ChannelMessageSend(ctx.ChannelID, formattedMessage)

	case command == string(Connect):
		v, err := tryConnectingToVoice(ctx.Session, ctx.GuildID, ctx.UserID, "")
		if err != nil {
			_, err := ctx.Session.ChannelMessageSend(ctx.ChannelID, "Error connecting to voice channel")
			if err != nil {
				log.Fatalf("Error connecting to voice: Error sending message: %v", err)
			}
			return
		}

		if v == nil {
			ctx.Session.ChannelMessageSendReply(ctx.ChannelID, "You need to be in a voice channel", uMsg.Reference())
			return
		}

	case command == string(PlaySound):
		if len(ctx.State.SoundList) == 0 {
			ctx.Session.ChannelMessageSend(ctx.ChannelID, "No sounds loaded")
			return
		}

		voice, err := tryConnectingToVoice(ctx.Session, ctx.GuildID, ctx.UserID, "")
		if err != nil {
			_, err := ctx.Session.ChannelMessageSend(ctx.ChannelID, "Error connecting to voice channel")
			if err != nil {
				log.Fatalf("Error connecting to voice: Error sending message: %v", err)
			}
			return
		}

		if voice == nil {
			ctx.Session.ChannelMessageSendReply(ctx.ChannelID, "You need to be in a voice channel", uMsg.Reference())
			return
		}

		//lookup sound locally only, upload or bootup should assure it's either here or nowhere
		mSplit := strings.Split(uMsg.Content, " ")

		if len(mSplit) != 2 {
			// TODO: handle this properly and call .help
			return
		}

		searchTerm := mSplit[1]
		sound, ok := ctx.State.SoundList[searchTerm]
		if !ok {
			ctx.Session.ChannelMessageSend(ctx.ChannelID, "Sound not found")
			return
		}

		go PlayAudioFile(voice, sound)

	case command == string(List):
		// shoutout rasmussy
		listOutput := "```(" + fmt.Sprint(len(ctx.State.SoundList)) + ") " + "Available sounds :\n------------------\n\n"
		nb := 0
		for name := range ctx.State.SoundList {
			nb += 1
			var soundName = string(name)
			for len(soundName) < 15 {
				soundName += " "
			}
			listOutput += soundName + "\t"
			if nb%6 == 0 {
				listOutput += "\n"
			}
			// Discord max message length is 2000
			if len(listOutput) > 1950 { // removed condition for max sounds printed
				listOutput += "```"
				ctx.Session.ChannelMessageSend(ctx.ChannelID, listOutput)
				listOutput = "```"
			}
		}
		listOutput += "```"
		if listOutput != "``````" {
			ctx.Session.ChannelMessageSend(ctx.ChannelID, listOutput)
		}

	case command == string(Rename):
		// find file by name, upload it with new name, delete old file
		searchTerm := strings.Split(uMsg.Content, " ")[1]
		newName := strings.Split(uMsg.Content, " ")[2]
		sound, ok := ctx.State.SoundList[searchTerm]
		if !ok {
			ctx.Session.ChannelMessageSend(ctx.ChannelID, "Sound not found")
			return
		}

		updatedMessage, updatedSound, err := reuploadSound(ctx, sound, searchTerm, newName)
		if updatedMessage == nil || updatedSound == nil || err != nil {
			_, err := ctx.Session.ChannelMessageSend(ctx.ChannelID, "Error reuploading sound")
			if err != nil {
				panic(err)
			}
			return
		}

		// this is ugly af, check if there's a better way to do this
		sound.MessageID = updatedMessage.ID
		ctx.State.SoundList[newName] = updatedSound
		delete(ctx.State.SoundList, searchTerm)

		ctx.Session.ChannelMessageSendReply(ctx.ChannelID, "Sound renamed", uMsg.Reference())

	case command == string(AddEntrance):
		searchTerm := strings.Split(uMsg.Content, " ")[1]

		sound, ok := ctx.State.SoundList[searchTerm]
		if !ok {
			ctx.Session.ChannelMessageSend(ctx.ChannelID, "Sound not found")
			return
		}

		soundMessage, err := ctx.Session.ChannelMessage(ctx.SoundsChannelID, sound.MessageID)
		if err != nil {
			_, err := ctx.Session.ChannelMessageSend(ctx.ChannelID, "Error getting sound message")
			if err != nil {
				panic(err)
			}
			return
		}

		if !soundMessage.Author.Bot {
			updatedMessage, updatedSound, err := reuploadSound(ctx, sound, searchTerm, "")
			if updatedMessage == nil || updatedSound == nil || err != nil {
				_, err := ctx.Session.ChannelMessageSend(ctx.ChannelID, "Error reuploading sound")
				if err != nil {
					panic(err)
				}
				return
			}
			soundMessage = updatedMessage
			// updatedMessage here sometimes doesn't have GuildID??? idk why
			ctx.State.SoundList[searchTerm] = updatedSound
			sound = updatedSound
		}

		userEntrance, ok := ctx.State.Entrances[ctx.UserID]
		if ok {
			if userEntrance.MessageID == sound.MessageID {
				ctx.Session.ChannelMessageSend(ctx.ChannelID, "This is already your entrance")
				return
			} else {
				delete(ctx.State.Entrances, ctx.UserID)
				oldEntranceMessage, err := ctx.Session.ChannelMessage(ctx.SoundsChannelID, userEntrance.MessageID)
				if err != nil {
					if strings.Contains(err.Error(), "HTTP 404") {
						delete(ctx.State.Entrances, ctx.UserID)
					} else {
						panic(err)
					}
				}

				// remove volume from message
				if oldEntranceMessage != nil && oldEntranceMessage.Content != "" {
					updatedTags := ""
					messageTags := strings.Split(oldEntranceMessage.Content, ";")
					for _, tag := range messageTags {
						if tag == "" {
							continue
						}

						typeValue := strings.Split(tag, ":")
						tagType := typeValue[0]

						if tagType == "e" {
							continue
						} else {
							updatedTags += tag + ";"
						}
					}

					oldEntranceMessage.Content = updatedTags
					_, err = ctx.Session.ChannelMessageEdit(ctx.SoundsChannelID, oldEntranceMessage.ID, oldEntranceMessage.Content)
					if err != nil {
						_, err := ctx.Session.ChannelMessageSend(ctx.ChannelID, "Error removing volume from old entrance")
						if err != nil {
							panic(err)
						}
						return
					}
				}
			}
		}

		// right now a message tag can look like "e:userID;v:0-100;e:userID;"
		// where e: says that sound is an entrance to that user and v: is the volume for that sound
		messageTags := strings.Split(soundMessage.Content, ";")
		if len(messageTags) > 0 {
			for _, tag := range messageTags {
				if tag == "" {
					continue
				}
				typeValue := strings.Split(tag, ":")
				tagType, tagValue := typeValue[0], typeValue[1]

				if tagType == "e" {
					if tagValue == ctx.UserID {
						ctx.Session.ChannelMessageSend(ctx.ChannelID, "This is already your entrance")
						return
					}
				}
			}

		}

		soundMessage.Content += "e:" + ctx.UserID + ";"
		ctx.Session.ChannelMessageEdit(ctx.SoundsChannelID, soundMessage.ID, soundMessage.Content)
		ctx.State.Entrances[ctx.UserID] = sound

		ctx.Session.ChannelMessageSendReply(ctx.ChannelID, "Entrance set", uMsg.Reference())

	case command == string(Adjustvol):
		searchTerm := strings.Split(uMsg.Content, " ")[1]
		volStr := strings.Split(uMsg.Content, " ")[2]
		volInt, err := strconv.ParseInt(volStr, 10, 64)
		if err != nil {
			_, err := ctx.Session.ChannelMessageSend(ctx.ChannelID, "Volume must be between 1 and 512 (0-200%)")
			if err != nil {
				panic(err)
			}
		}

		if volInt < 0 || volInt > 512 {
			ctx.Session.ChannelMessageSend(ctx.ChannelID, "Volume must be between 1 and 512 (0-200%)")
			return
		}

		sound, ok := ctx.State.SoundList[searchTerm]
		if !ok {
			ctx.Session.ChannelMessageSend(ctx.ChannelID, "Sound not found")
			return
		}

		soundMessage, err := ctx.Session.ChannelMessage(ctx.SoundsChannelID, sound.MessageID)
		if err != nil {
			panic(err)
		}

		// make function for this
		if !soundMessage.Author.Bot {
			updatedMessage, updatedSound, err := reuploadSound(ctx, sound, searchTerm, "")
			if updatedMessage == nil || updatedSound == nil || err != nil {
				_, err := ctx.Session.ChannelMessageSend(ctx.ChannelID, "Error reuploading sound")
				if err != nil {
					panic(err)
				}
				return
			}
			soundMessage = updatedMessage
			ctx.State.SoundList[searchTerm] = updatedSound
			sound = updatedSound
		}

		updatedTags := ""
		if soundMessage.Content != "" {
			messageTags := strings.Split(soundMessage.Content, ";")
			for _, tag := range messageTags {
				if tag == "" {
					continue
				}

				typeValue := strings.Split(tag, ":")
				tagType := typeValue[0]

				if tagType == "v" {
					continue
				} else {
					updatedTags += tag + ";"
				}
			}
		}

		updatedTags += "v:" + volStr + ";"
		soundMessage.Content = updatedTags
		_, err = ctx.Session.ChannelMessageEdit(ctx.SoundsChannelID, soundMessage.ID, soundMessage.Content)
		if err != nil {
			panic(err)
		}
		ctx.State.SoundList[searchTerm].Volume = int(volInt)
		ctx.Session.ChannelMessageSendReply(ctx.ChannelID, "Volume adjusted", uMsg.Reference())

	case command == string(Find):
		searchTerm := strings.Split(uMsg.Content, " ")[1]
		sound, ok := ctx.State.SoundList[searchTerm]
		if !ok {
			ctx.Session.ChannelMessageSend(ctx.ChannelID, "Sound not found")
			return
		}

		messageLink := "https://discordapp.com/channels/" + ctx.GuildID + "/" + ctx.SoundsChannelID + "/" + sound.MessageID
		messageMarkdown := "Found this: [" + searchTerm + "](" + messageLink + ")"
		ctx.Session.ChannelMessageSendReply(ctx.ChannelID, messageMarkdown, ctx.UserMessage.Reference())
	}
}

// // This is most likely not the best way to do this
// // if message has a zip file, extract it and send every .mp3 file to the sounds channel
// // delete the original message and the files written to disk
func handleZipUpload(ctx *Context, attachment *discordgo.MessageAttachment) {
	err := os.Mkdir("sounds", 0755)
	if err != nil {
		if !os.IsExist(err) {
			_, err := ctx.Session.ChannelMessageSend(ctx.ChannelID, "Error handling zip upload")
			if err != nil {
				panic(err)
			}
			return
		}
	}

	filePath := filepath.Join("sounds", attachment.Filename)
	downloadFile(attachment.Filename, attachment.URL)

	archive, err := zip.OpenReader(filePath)
	if err != nil {
		panic(err)
	}
	defer archive.Close()

	for _, file := range archive.File {
		if strings.Split(file.Name, ".")[1] == "mp3" {
			fileReader, err := file.Open()
			if err != nil {
				panic(err)
			}

			filePath := "sounds/" + file.Name
			fileWriter, err := os.Create(filePath)
			if err != nil {
				panic(err)
			}

			_, err = ctx.Session.ChannelFileSend(ctx.ChannelID, file.Name, fileReader)
			if err != nil {
				panic(err)
			}

			fileWriter.Close()
			fileReader.Close()
		}
	}

	err = os.RemoveAll("sounds")
	if err != nil {
		panic(err)
	}
	ctx.Session.ChannelMessageDelete(ctx.ChannelID, ctx.UserMessage.ID)
}

// discord rate limit's at around 4/5 quick requests and this does 1 per 100 sounds (4 at the current 390 sounds)
// loads sounds and entrances to memory
func getSoundsRecursive(d *discordgo.Session, guildID string, beforeID string) error {
	soundsChannelID, err := getSoundsChannelID(d, guildID)
	if err != err {
		panic(err)
	}
	channelMessages, err := d.ChannelMessages(soundsChannelID, 100, beforeID, "", "")
	if err != nil {
		return err
	}

	for _, channelMessage := range channelMessages {
		if len(channelMessage.Attachments) > 0 {
			fileName := channelMessage.Attachments[0].Filename
			if strings.Split(fileName, ".")[1] != "mp3" {
				continue
			}
			trimmedName := strings.TrimSuffix(fileName, ".mp3")

			sound := &Sound{
				MessageID: channelMessage.ID,
				URL:       channelMessage.Attachments[0].URL,
			}

			if channelMessage.Content != "" {
				messageTags := strings.Split(channelMessage.Content, ";")

				for _, tag := range messageTags {
					if tag == "" {
						continue
					}

					tag := strings.Split(tag, ":")
					tagType, tagValue := tag[0], tag[1]

					if tagType == "e" {
						// tagValue is the user ID
						store[guildID].Entrances[tagValue] = sound
					}

					if tagType == "v" {
						// tagValue is the volume
						volInt, err := strconv.ParseInt(tagValue, 10, 64)
						if err != nil {
							panic(err)
						}
						sound.Volume = int(volInt)
					}
				}
			}
			store[guildID].SoundList[trimmedName] = sound
		}
	}

	// if length < 100, this is the last batch and checked all of them
	if len(channelMessages) < 100 {
		return nil
	}

	lastMessageID := channelMessages[len(channelMessages)-1].ID
	return getSoundsRecursive(d, guildID, lastMessageID)
}

func getSoundsChannelID(d *discordgo.Session, guildID string) (string, error) {
	// try using ctx here
	channels, err := d.GuildChannels(guildID)
	if err != nil {
		return "", err
	}

	for _, channel := range channels {
		if channel.Name == SoundsChannel {
			return channel.ID, nil
		}
	}

	return "", errors.New("no 'sounds' channel found")
}

func getUsersInVC(ctx *Context) {
	currentGuild, err := ctx.Session.State.Guild(ctx.GuildID)
	if err != nil {
		panic(err)
	}

	voiceChannelsMap := make(map[string]*VoiceChannel)
	for _, channel := range currentGuild.Channels {
		if channel.Type == discordgo.ChannelTypeGuildVoice {
			voiceChannelsMap[channel.ID] = &VoiceChannel{
				ID:             channel.ID,
				GuildID:        ctx.GuildID,
				Name:           channel.Name,
				UsersConnected: []discordgo.User{},
			}
		}
	}

	for _, vs := range currentGuild.VoiceStates {
		if vc, ok := voiceChannelsMap[vs.ChannelID]; ok {
			user, err := ctx.Session.User(vs.UserID)
			if err != nil {
				fmt.Println("Error getting user:", err)
				continue
			}
			if !user.Bot {
				vc.UsersConnected = append(vc.UsersConnected, *user)
			}
		}
	}

	var Channels Channels
	for _, vc := range voiceChannelsMap {
		Channels.VoiceChannels = append(Channels.VoiceChannels, *vc)
	}

	ctx.State.Channels = Channels
}

func voiceStateUpdate(d *discordgo.Session, v *discordgo.VoiceStateUpdate) {
	ctx, err := getContext(d, nil, v)
	if err != nil {
		panic(err)
	}

	if len(ctx.State.Channels.VoiceChannels) == 0 {
		getUsersInVC(ctx)
	}

	voiceChannelStateUpdate(ctx)
	// plays entrance if user joins a voice channel, doesn't on switch
	if v.ChannelID != "" && v.BeforeUpdate == nil {
		userEntrance, ok := ctx.State.Entrances[ctx.UserID]
		if ok {
			voice, err := tryConnectingToVoice(d, v.GuildID, v.UserID, v.ChannelID)
			if err != nil {
				panic(err)
			}

			if voice == nil {
				return
			}

			go PlayAudioFile(voice, userEntrance)
		}
	}
}

func voiceChannelStateUpdate(ctx *Context) {
loop:
	for idx, vc := range ctx.State.Channels.VoiceChannels {
		for i, user := range vc.UsersConnected {
			if user.ID == ctx.UserID {
				ctx.State.Channels.VoiceChannels[idx].UsersConnected = append(vc.UsersConnected[:i], vc.UsersConnected[i+1:]...)
				break loop
			}
		}
	}

	if ctx.ChannelID != "" {
		for idx, vc := range ctx.State.Channels.VoiceChannels {
			if vc.ID == ctx.ChannelID {
				user, err := ctx.Session.User(ctx.UserID)
				if err != nil {
					fmt.Println("Error getting user:", err)
					return
				}
				ctx.State.Channels.VoiceChannels[idx].UsersConnected = append(vc.UsersConnected, *user)
				return
			}
		}
	}
}

func reuploadSound(ctx *Context, sound *Sound, searchTerm string, fileName string) (*discordgo.Message, *Sound, error) {
	req, err := http.Get(sound.URL)
	if err != nil {
		return nil, nil, err
	}
	defer req.Body.Close()

	oldMessage, err := ctx.Session.ChannelMessage(ctx.SoundsChannelID, sound.MessageID)
	if err != nil {
		return nil, nil, err
	}

	if strings.Contains(oldMessage.Content, "e:") {
		for _, tag := range strings.Split(oldMessage.Content, ";") {
			if tag == "" {
				continue
			}

			typeValue := strings.Split(tag, ":")
			tagType, tagValue := typeValue[0], typeValue[1]

			if tagType == "e" {
				ctx.State.Entrances[tagValue] = sound
			}
		}
	}

	if fileName == "" {
		fileName = searchTerm
	}
	soundMessage, err := ctx.Session.ChannelMessageSendComplex(ctx.SoundsChannelID, &discordgo.MessageSend{
		Content: oldMessage.Content,
		Files: []*discordgo.File{
			{
				Name:   fileName + ".mp3",
				Reader: req.Body,
			},
		},
	})
	if err != nil {
		return nil, nil, err
	}

	err = ctx.Session.ChannelMessageDelete(ctx.SoundsChannelID, sound.MessageID)
	if err != nil {
		return nil, nil, err
	}

	updatedSound := &Sound{
		MessageID: soundMessage.ID,
		URL:       soundMessage.Attachments[0].URL,
		Volume:    sound.Volume,
	}

	return soundMessage, updatedSound, nil
}

func readyHandler(d *discordgo.Session, ready *discordgo.Ready) {
	for _, guild := range ready.Guilds {
		if store == nil {
			store = make(Store)
		}

		store[guild.ID] = State{
			SoundList: make(SoundList),
			Entrances: make(Entrances),
			Channels: Channels{
				VoiceChannels: []VoiceChannel{},
			},
		}

		// try using ctx here
		err := getSoundsRecursive(d, guild.ID, "")
		if err != nil {
			_, err := d.ChannelMessageSend(guild.ID, "Error loading sounds")
			if err != nil {
				panic(err)
			}
		}
	}
}
