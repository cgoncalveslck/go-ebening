package bot

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
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

type SoundName string
type UserID string
type GuildID string
type SoundList map[SoundName]*Sound
type Command string
type ChannelName string
type State struct {
	SoundList SoundList
	Entrances map[UserID]*Sound
	Channels  Channels
}

type Store map[GuildID]State

type Context struct {
	GuildID GuildID
	UserID  UserID
	State   State
}

// SoundName as key bc that's what we're using for lookup

type Sound struct {
	MessageID string
	URL       string
	Volume    int // dca uses 0-256 for some reason, try mapping it to 0-100 for better UX
}

type Channels struct {
	VoiceChannels []VoiceChannel
}
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
	SoundsChannel   ChannelName = "sounds"
	CommandsChannel ChannelName = "bot-commands"
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
	discord, err := discordgo.New("Bot " + BotToken)
	if err != nil {
		panic(err)
	}

	State = GuildState{
		SoundList: make(SoundList),
		Entrances: make(map[UserID]*Sound),
		Channels: Channels{
			VoiceChannels: []VoiceChannel{},
		},
	}

	discord.AddHandler(func(d *discordgo.Session, ready *discordgo.Ready) {
		for _, guild := range ready.Guilds {
			if State.SoundList[GuildID(guild.ID)] == nil {
				State.SoundList[GuildID(guild.ID)] = make(SoundList)
			}
			err := getSoundsRecursive(discord, guild.ID, "")
			if err != nil {
				_, err := discord.ChannelMessageSend(guild.ID, "Error loading sounds")
				if err != nil {
					panic(err)
				}
			}
		}
	})

	discord.AddHandler(voiceStateUpdate)
	discord.AddHandler(newMessage)

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
//  on reupload, keep volume and entrances
//  optimize memory usage (pointers etc)

// BIG REFACTOR NEEDED
//  https://excalidraw.com/#json=cJufcyaiOy18NM6cQ-u-o,o07HMkjbMa0FxopxXiWB3A
// 	keep state per Guild (sounds, entrances, volumes, etc)
//  can/should be abstracted/implicit by assuming x values are certain in lifetime of event (userID, ChannelID, GuildID)
// 	in practice we should use "State.SoundList" and the underlying value is actually like Store[GuildID].State.SoundList or something
//	also make functions/methods for managing state like getSound(name), implicitly should already know your id, server and channel
// 	instead of doing Store[GuildID].State.SoundList[SoundName] for every state access, you get the point

func newMessage(discord *discordgo.Session, userMessage *discordgo.MessageCreate) {
	if userMessage.Author.Bot {
		return
	}

	// for debugging, delete eventually
	if userMessage.Content == ".reset" && userMessage.Author.ID == "123475794503794690" {
		//delete all messages in channel
		soundsChannel := getSoundsChannelID(discord, userMessage.GuildID)
		messages, err := discord.ChannelMessages(soundsChannel, 100, "", "", "")
		if err != nil {
			log.Fatalf("Error getting messages: %v", err)
		}

		for _, message := range messages {
			discord.ChannelMessageDelete(soundsChannel, message.ID)
		}
	}

	channel, err := discord.Channel(userMessage.ChannelID)
	if err != nil {
		log.Fatalf("Error getting channel: %v", err)
	}

	if channel.Name == string(SoundsChannel) {
		handleSoundsChannel(discord, userMessage)
	}

	if channel.Name == string(CommandsChannel) {
		handleCommandsChannel(discord, userMessage)
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

func handleSoundsChannel(discord *discordgo.Session, userMessage *discordgo.MessageCreate) {
	if len(userMessage.Attachments) > 0 {
		for _, attachment := range userMessage.Attachments {
			if strings.Split(attachment.Filename, ".")[1] == "zip" {
				handleZipUpload(discord, userMessage, attachment)
			}

			if strings.Split(attachment.Filename, ".")[1] == "mp3" {
				State.SoundList[GuildID(userMessage.GuildID)][SoundName(strings.TrimSuffix(attachment.Filename, ".mp3"))] = &Sound{
					MessageID: userMessage.ID,
					URL:       attachment.URL,
				}
			}
		}
	} else {
		m, err := discord.ChannelMessageSendReply(userMessage.ChannelID, "Please use this channel for files only", userMessage.Reference())
		if err != nil {
			log.Fatalf("Error sending message: %v", err)
		}
		time.Sleep(3 * time.Second)
		discord.ChannelMessageDelete(userMessage.ChannelID, m.ID)
		discord.ChannelMessageDelete(userMessage.ChannelID, userMessage.ID)
	}
}

func handleCommandsChannel(discord *discordgo.Session, userMessage *discordgo.MessageCreate) {
	if len(userMessage.Attachments) > 0 {
		discord.ChannelMessageSendReply(userMessage.ChannelID, "If you're tring to upload a sound, put it in the 'sounds' channel", userMessage.Reference())
	}

	// skips the current sound only if the message is exactly ".ss" to avoid accidental skips
	if userMessage.Content == string(SkipSound) {
		stopPlayback <- true
		return
	}

	command := strings.Split(userMessage.Content, " ")[0]
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

		discord.ChannelMessageSend(userMessage.ChannelID, formattedMessage)

	case command == string(Connect):
		v, err := tryConnectingToVoice(discord, userMessage.GuildID, userMessage.Author.ID, "")
		if err != nil {
			_, err := discord.ChannelMessageSend(userMessage.ChannelID, "Error connecting to voice channel")
			if err != nil {
				log.Fatalf("Error connecting to voice: Error sending message: %v", err)
			}
			return
		}

		if v == nil {
			discord.ChannelMessageSendReply(userMessage.ChannelID, "You need to be in a voice channel", userMessage.Reference())
			return
		}

	case command == string(PlaySound):
		if len(State.SoundList) == 0 {
			discord.ChannelMessageSend(userMessage.ChannelID, "No sounds loaded")
			return
		}

		voice, err := tryConnectingToVoice(discord, userMessage.GuildID, userMessage.Author.ID, "")
		if err != nil {
			_, err := discord.ChannelMessageSend(userMessage.ChannelID, "Error connecting to voice channel")
			if err != nil {
				log.Fatalf("Error connecting to voice: Error sending message: %v", err)
			}
			return
		}

		if voice == nil {
			discord.ChannelMessageSendReply(userMessage.ChannelID, "You need to be in a voice channel", userMessage.Reference())
			return
		}

		//lookup sound locally only, upload or bootup should assure it's either here or nowhere
		searchTerm := strings.Split(userMessage.Content, " ")[1]
		sound, ok := State.SoundList[GuildID(userMessage.GuildID)][SoundName(searchTerm)]
		if !ok {
			discord.ChannelMessageSend(userMessage.ChannelID, "Sound not found")
			return
		}

		go PlayAudioFile(voice, sound)

	case command == string(List):
		// shoutout rasmussy
		listOutput := "```(" + fmt.Sprint(len(State.SoundList[GuildID(userMessage.GuildID)])) + ") " + "Available sounds :\n------------------\n\n"
		nb := 0
		for name := range State.SoundList[GuildID(userMessage.GuildID)] {
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
				discord.ChannelMessageSend(userMessage.ChannelID, listOutput)
				listOutput = "```"
			}
		}
		listOutput += "```"
		if listOutput != "``````" {
			discord.ChannelMessageSend(userMessage.ChannelID, listOutput)
		}

	case command == string(Rename):
		// find file by name, upload it with new name, delete old file
		searchTerm := strings.Split(userMessage.Content, " ")[1]
		newName := strings.Split(userMessage.Content, " ")[2]
		sound, ok := State.SoundList[GuildID(userMessage.GuildID)][SoundName(searchTerm)]
		if !ok {
			discord.ChannelMessageSend(userMessage.ChannelID, "Sound not found")
			return
		}

		soundsChannel := getSoundsChannelID(discord, userMessage.GuildID)
		updatedMessage, updatedSound, err := reuploadSound(discord, sound, soundsChannel, searchTerm, newName)
		if updatedMessage == nil || updatedSound == nil || err != nil {
			_, err := discord.ChannelMessageSend(userMessage.ChannelID, "Error reuploading sound")
			if err != nil {
				panic(err)
			}
			return
		}

		// this is ugly af, check if there's a better way to do this
		sound.MessageID = updatedMessage.ID
		State.SoundList[GuildID(userMessage.GuildID)][SoundName(newName)] = updatedSound
		delete(State.SoundList[GuildID(userMessage.GuildID)], SoundName(searchTerm))

		discord.ChannelMessageSendReply(userMessage.ChannelID, "Sound renamed", userMessage.Reference())

	case command == string(AddEntrance):
		searchTerm := strings.Split(userMessage.Content, " ")[1]

		sound, ok := State.SoundList[GuildID(userMessage.GuildID)][SoundName(searchTerm)]
		if !ok {
			discord.ChannelMessageSend(userMessage.ChannelID, "Sound not found")
			return
		}

		soundsChannel := getSoundsChannelID(discord, userMessage.GuildID)
		soundMessage, err := discord.ChannelMessage(soundsChannel, sound.MessageID)
		if err != nil {
			_, err := discord.ChannelMessageSend(userMessage.ChannelID, "Error getting sound message")
			if err != nil {
				panic(err)
			}
			return
		}

		if !soundMessage.Author.Bot {
			updatedMessage, updatedSound, err := reuploadSound(discord, sound, soundsChannel, searchTerm, "")
			if updatedMessage == nil || updatedSound == nil || err != nil {
				_, err := discord.ChannelMessageSend(userMessage.ChannelID, "Error reuploading sound")
				if err != nil {
					panic(err)
				}
				return
			}
			soundMessage = updatedMessage
			// updatedMessage here sometimes doesn't have GuildID??? idk why
			State.SoundList[GuildID(userMessage.GuildID)][SoundName(searchTerm)] = updatedSound
			sound = updatedSound
		}

		userEntrance, ok := State.Entrances[UserID(userMessage.Author.ID)]
		if ok {
			if userEntrance.MessageID == sound.MessageID {
				discord.ChannelMessageSend(userMessage.ChannelID, "This is already your entrance")
				return
			} else {
				delete(State.Entrances, UserID(userMessage.Author.ID))
				oldEntranceMessage, err := discord.ChannelMessage(soundsChannel, userEntrance.MessageID)
				if err != nil {
					if strings.Contains(err.Error(), "HTTP 404") {
						delete(State.Entrances, UserID(userMessage.Author.ID))
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
					_, err = discord.ChannelMessageEdit(soundsChannel, oldEntranceMessage.ID, oldEntranceMessage.Content)
					if err != nil {
						_, err := discord.ChannelMessageSend(userMessage.ChannelID, "Error removing volume from old entrance")
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
					if tagValue == userMessage.Author.ID {
						discord.ChannelMessageSend(userMessage.ChannelID, "This is already your entrance")
						return
					}
				}
			}

		}

		soundMessage.Content += "e:" + userMessage.Author.ID + ";"
		discord.ChannelMessageEdit(soundsChannel, soundMessage.ID, soundMessage.Content)
		State.Entrances[UserID(userMessage.Author.ID)] = sound

		discord.ChannelMessageSendReply(userMessage.ChannelID, "Entrance set", userMessage.Reference())

	case command == string(Adjustvol):
		searchTerm := strings.Split(userMessage.Content, " ")[1]
		volStr := strings.Split(userMessage.Content, " ")[2]
		volInt, err := strconv.ParseInt(volStr, 10, 64)
		if err != nil {
			_, err := discord.ChannelMessageSend(userMessage.ChannelID, "Volume must be between 1 and 512 (0-200%)")
			if err != nil {
				panic(err)
			}
		}

		if volInt < 0 || volInt > 512 {
			discord.ChannelMessageSend(userMessage.ChannelID, "Volume must be between 1 and 512 (0-200%)")
			return
		}

		sound, ok := State.SoundList[GuildID(userMessage.GuildID)][SoundName(searchTerm)]
		if !ok {
			discord.ChannelMessageSend(userMessage.ChannelID, "Sound not found")
			return
		}

		soundsChannel := getSoundsChannelID(discord, userMessage.GuildID)
		soundMessage, err := discord.ChannelMessage(soundsChannel, sound.MessageID)
		if err != nil {
			panic(err)
		}

		// make function for this
		if !soundMessage.Author.Bot {
			updatedMessage, updatedSound, err := reuploadSound(discord, sound, soundsChannel, searchTerm, "")
			if updatedMessage == nil || updatedSound == nil || err != nil {
				_, err := discord.ChannelMessageSend(userMessage.ChannelID, "Error reuploading sound")
				if err != nil {
					panic(err)
				}
				return
			}
			soundMessage = updatedMessage
			State.SoundList[GuildID(userMessage.GuildID)][SoundName(searchTerm)] = updatedSound
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
		_, err = discord.ChannelMessageEdit(soundsChannel, soundMessage.ID, soundMessage.Content)
		if err != nil {
			panic(err)
		}
		State.SoundList[GuildID(userMessage.GuildID)][SoundName(searchTerm)].Volume = int(volInt)
		discord.ChannelMessageSendReply(userMessage.ChannelID, "Volume adjusted", userMessage.Reference())

	case command == string(Find):
		searchTerm := strings.Split(userMessage.Content, " ")[1]
		sound, ok := State.SoundList[GuildID(userMessage.GuildID)][SoundName(searchTerm)]
		if !ok {
			discord.ChannelMessageSend(userMessage.ChannelID, "Sound not found")
			return
		}

		messageLink := "https://discordapp.com/channels/" + userMessage.GuildID + "/" + getSoundsChannelID(discord, userMessage.GuildID) + "/" + sound.MessageID
		messageMarkdown := "Found this: [" + searchTerm + "](" + messageLink + ")"
		discord.ChannelMessageSendReply(userMessage.ChannelID, messageMarkdown, userMessage.Reference())
	}
}

// This is most likely not the best way to do this
// if message has a zip file, extract it and send every .mp3 file to the sounds channel
// delete the original message and the files written to disk
func handleZipUpload(d *discordgo.Session, userMessage *discordgo.MessageCreate, attachment *discordgo.MessageAttachment) {
	err := os.Mkdir("sounds", 0755)
	if err != nil {
		if !os.IsExist(err) {
			_, err := d.ChannelMessageSend(userMessage.ChannelID, "Error handling zip upload")
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

			_, err = d.ChannelFileSend(userMessage.ChannelID, file.Name, fileReader)
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
	d.ChannelMessageDelete(userMessage.ChannelID, userMessage.ID)
}

// discord rate limit's at around 4/5 quick requests and this does 1 per 100 sounds (4 at the current 390 sounds)
// loads sounds and entrances to memory
func getSoundsRecursive(d *discordgo.Session, guildID string, beforeID string) error {
	soundsChannelID := getSoundsChannelID(d, guildID)
	if soundsChannelID == "" {
		return errors.New("sounds channel not found")
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
						State.Entrances[UserID(tagValue)] = sound
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

			State.SoundList[GuildID(guildID)][SoundName(trimmedName)] = sound
		}
	}

	// if length < 100, this is the last batch and checked all of them
	if len(channelMessages) < 100 {
		return nil
	}

	lastMessageID := channelMessages[len(channelMessages)-1].ID
	return getSoundsRecursive(d, guildID, lastMessageID)
}

func getSoundsChannelID(d *discordgo.Session, guildID string) string {
	channels, err := d.GuildChannels(guildID)
	if err != nil {
		panic(err)
	}

	for _, channel := range channels {
		if channel.Name == string(SoundsChannel) {
			return channel.ID
		}
	}

	return ""
}

// Redo this
func getUsersInVC(d *discordgo.Session, guildID string) {
	currentGuild, err := d.State.Guild(guildID)
	if err != nil {
		panic(err)
	}

	voiceChannelsMap := make(map[string]*VoiceChannel)
	for _, channel := range currentGuild.Channels {
		if channel.Type == discordgo.ChannelTypeGuildVoice {
			voiceChannelsMap[channel.ID] = &VoiceChannel{
				ID:             channel.ID,
				GuildID:        guildID,
				Name:           channel.Name,
				UsersConnected: []discordgo.User{},
			}
		}
	}

	for _, vs := range currentGuild.VoiceStates {
		if vc, ok := voiceChannelsMap[vs.ChannelID]; ok {
			user, err := d.User(vs.UserID)
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

	State.Channels = Channels
}

func voiceStateUpdate(d *discordgo.Session, v *discordgo.VoiceStateUpdate) {
	if len(State.Channels.VoiceChannels) == 0 {
		getUsersInVC(d, v.GuildID)
	}

	voiceChannelStateUpdate(d, v.UserID, v.ChannelID)
	// plays entrance if user joins a voice channel, doesn't on switch
	if v.ChannelID != "" && v.BeforeUpdate == nil {
		userEntrance, ok := State.Entrances[UserID(v.UserID)]
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

func voiceChannelStateUpdate(d *discordgo.Session, userID string, channelID string) {
loop:
	for idx, vc := range State.Channels.VoiceChannels {
		for i, user := range vc.UsersConnected {
			if user.ID == userID {
				State.Channels.VoiceChannels[idx].UsersConnected = append(vc.UsersConnected[:i], vc.UsersConnected[i+1:]...)
				break loop
			}
		}
	}

	if channelID != "" {
		for idx, vc := range State.Channels.VoiceChannels {
			if vc.ID == channelID {
				user, err := d.User(userID)
				if err != nil {
					fmt.Println("Error getting user:", err)
					return
				}
				State.Channels.VoiceChannels[idx].UsersConnected = append(vc.UsersConnected, *user)
				return
			}
		}
	}
}

func reuploadSound(discord *discordgo.Session, sound *Sound, soundsChannel, searchTerm string, fileName string) (*discordgo.Message, *Sound, error) {
	req, err := http.Get(sound.URL)
	if err != nil {
		return nil, nil, err
	}
	defer req.Body.Close()

	oldMessage, err := discord.ChannelMessage(soundsChannel, sound.MessageID)
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
				State.Entrances[UserID(tagValue)] = sound
			}
		}
	}

	if fileName == "" {
		fileName = searchTerm
	}
	soundMessage, err := discord.ChannelMessageSendComplex(soundsChannel, &discordgo.MessageSend{
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

	err = discord.ChannelMessageDelete(soundsChannel, sound.MessageID)
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
