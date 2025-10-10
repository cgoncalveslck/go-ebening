package bot

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// StorageBackend defines the interface for sound storage
type StorageBackend interface {
	// LoadSounds loads all sounds from storage into memory
	LoadSounds(d *discordgo.Session, guildID string, soundsChannelID string) (SoundList, Entrances, error)
	
	// SaveSound saves a new sound to storage
	SaveSound(d *discordgo.Session, ctx *Context, fileName string, reader io.Reader) (*Sound, error)
	
	// DeleteSound removes a sound from storage
	DeleteSound(d *discordgo.Session, ctx *Context, sound *Sound) error
	
	// UpdateSoundMetadata updates metadata (tags) for a sound
	UpdateSoundMetadata(d *discordgo.Session, ctx *Context, sound *Sound, tags string) error
	
	// GetSoundMetadata retrieves metadata (tags) for a sound
	GetSoundMetadata(d *discordgo.Session, ctx *Context, sound *Sound) (string, error)
}

// DiscordStorage implements StorageBackend using Discord channels
type DiscordStorage struct{}

// VolumeStorage implements StorageBackend using Railway volumes
type VolumeStorage struct {
	BasePath string
}

// NewStorageBackend creates the appropriate storage backend based on environment
func NewStorageBackend() StorageBackend {
	storageMode := os.Getenv("STORAGE_MODE")
	if storageMode == "volume" {
		volumePath := os.Getenv("VOLUME_PATH")
		if volumePath == "" {
			volumePath = "/data/sounds"
		}
		log.Printf("Using volume storage at: %s", volumePath)
		return &VolumeStorage{BasePath: volumePath}
	}
	log.Println("Using Discord channel storage")
	return &DiscordStorage{}
}

// DiscordStorage implementation

func (ds *DiscordStorage) LoadSounds(d *discordgo.Session, guildID string, soundsChannelID string) (SoundList, Entrances, error) {
	soundList := make(SoundList)
	entrances := make(Entrances)
	
	err := ds.getSoundsRecursive(d, guildID, soundsChannelID, "", soundList, entrances)
	return soundList, entrances, err
}

func (ds *DiscordStorage) getSoundsRecursive(d *discordgo.Session, guildID string, soundsChannelID string, beforeID string, soundList SoundList, entrances Entrances) error {
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
						entrances[tagValue] = sound
					}

					if tagType == "v" {
						volInt, err := strconv.ParseInt(tagValue, 10, 64)
						if err != nil {
							log.Printf("Error parsing volume: %v", err)
							continue
						}
						sound.Volume = int(volInt)
					}
				}
			}
			soundList[trimmedName] = sound
		}
	}

	if len(channelMessages) < 100 {
		return nil
	}

	lastMessageID := channelMessages[len(channelMessages)-1].ID
	return ds.getSoundsRecursive(d, guildID, soundsChannelID, lastMessageID, soundList, entrances)
}

func (ds *DiscordStorage) SaveSound(d *discordgo.Session, ctx *Context, fileName string, reader io.Reader) (*Sound, error) {
	soundMessage, err := d.ChannelMessageSendComplex(ctx.SoundsChannelID, &discordgo.MessageSend{
		Files: []*discordgo.File{
			{
				Name:   fileName + ".mp3",
				Reader: reader,
			},
		},
	})
	if err != nil {
		return nil, err
	}

	sound := &Sound{
		MessageID: soundMessage.ID,
		URL:       soundMessage.Attachments[0].URL,
	}

	return sound, nil
}

func (ds *DiscordStorage) DeleteSound(d *discordgo.Session, ctx *Context, sound *Sound) error {
	return d.ChannelMessageDelete(ctx.SoundsChannelID, sound.MessageID)
}

func (ds *DiscordStorage) UpdateSoundMetadata(d *discordgo.Session, ctx *Context, sound *Sound, tags string) error {
	_, err := d.ChannelMessageEdit(ctx.SoundsChannelID, sound.MessageID, tags)
	return err
}

func (ds *DiscordStorage) GetSoundMetadata(d *discordgo.Session, ctx *Context, sound *Sound) (string, error) {
	message, err := d.ChannelMessage(ctx.SoundsChannelID, sound.MessageID)
	if err != nil {
		return "", err
	}
	return message.Content, nil
}

// VolumeStorage implementation

func (vs *VolumeStorage) LoadSounds(d *discordgo.Session, guildID string, soundsChannelID string) (SoundList, Entrances, error) {
	soundList := make(SoundList)
	entrances := make(Entrances)

	guildPath := filepath.Join(vs.BasePath, guildID)
	
	// Create directory if it doesn't exist
	if err := os.MkdirAll(guildPath, 0755); err != nil {
		return soundList, entrances, err
	}

	// Read all mp3 files
	entries, err := os.ReadDir(guildPath)
	if err != nil {
		return soundList, entrances, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		
		fileName := entry.Name()
		if !strings.HasSuffix(fileName, ".mp3") {
			continue
		}

		soundName := strings.TrimSuffix(fileName, ".mp3")
		soundPath := filepath.Join(guildPath, fileName)
		
		sound := &Sound{
			MessageID: soundName, // Use sound name as ID for volume storage
			URL:       soundPath,  // Store local file path
		}

		// Read metadata file if exists
		metadataPath := soundPath + ".meta"
		if metadataBytes, err := os.ReadFile(metadataPath); err == nil {
			metadata := string(metadataBytes)
			messageTags := strings.Split(metadata, ";")

			for _, tag := range messageTags {
				if tag == "" {
					continue
				}

				parts := strings.Split(tag, ":")
				if len(parts) != 2 {
					continue
				}
				tagType, tagValue := parts[0], parts[1]

				if tagType == "e" {
					entrances[tagValue] = sound
				}

				if tagType == "v" {
					volInt, err := strconv.ParseInt(tagValue, 10, 64)
					if err != nil {
						log.Printf("Error parsing volume: %v", err)
						continue
					}
					sound.Volume = int(volInt)
				}
			}
		}

		soundList[soundName] = sound
	}

	return soundList, entrances, nil
}

func (vs *VolumeStorage) SaveSound(d *discordgo.Session, ctx *Context, fileName string, reader io.Reader) (*Sound, error) {
	guildPath := filepath.Join(vs.BasePath, ctx.GuildID)
	
	// Create directory if it doesn't exist
	if err := os.MkdirAll(guildPath, 0755); err != nil {
		return nil, err
	}

	soundPath := filepath.Join(guildPath, fileName+".mp3")
	
	file, err := os.Create(soundPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if _, err := io.Copy(file, reader); err != nil {
		return nil, err
	}

	sound := &Sound{
		MessageID: fileName,
		URL:       soundPath,
	}

	return sound, nil
}

func (vs *VolumeStorage) DeleteSound(d *discordgo.Session, ctx *Context, sound *Sound) error {
	// Delete both the sound file and its metadata
	if err := os.Remove(sound.URL); err != nil && !os.IsNotExist(err) {
		return err
	}
	
	metadataPath := sound.URL + ".meta"
	if err := os.Remove(metadataPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	
	return nil
}

func (vs *VolumeStorage) UpdateSoundMetadata(d *discordgo.Session, ctx *Context, sound *Sound, tags string) error {
	metadataPath := sound.URL + ".meta"
	return os.WriteFile(metadataPath, []byte(tags), 0644)
}

func (vs *VolumeStorage) GetSoundMetadata(d *discordgo.Session, ctx *Context, sound *Sound) (string, error) {
	metadataPath := sound.URL + ".meta"
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// Helper function to reupload sound with new storage backend
func reuploadSoundWithStorage(storage StorageBackend, ctx *Context, sound *Sound, searchTerm string, fileName string) (*Sound, error) {
	// Get the sound data
	var reader io.ReadCloser
	var err error
	
	if strings.HasPrefix(sound.URL, "http") {
		// Discord storage - download from URL
		resp, err := http.Get(sound.URL)
		if err != nil {
			return nil, err
		}
		reader = resp.Body
	} else {
		// Volume storage - read from file
		file, err := os.Open(sound.URL)
		if err != nil {
			return nil, err
		}
		reader = file
	}
	defer reader.Close()

	// Get old metadata
	oldMetadata, err := storage.GetSoundMetadata(ctx.Session, ctx, sound)
	if err != nil {
		log.Printf("Warning: could not get metadata: %v", err)
		oldMetadata = ""
	}

	// Parse entrances from old metadata
	if strings.Contains(oldMetadata, "e:") {
		for _, tag := range strings.Split(oldMetadata, ";") {
			if tag == "" {
				continue
			}

			typeValue := strings.Split(tag, ":")
			if len(typeValue) != 2 {
				continue
			}
			tagType, tagValue := typeValue[0], typeValue[1]

			if tagType == "e" {
				ctx.State.Entrances[tagValue] = sound
			}
		}
	}

	if fileName == "" {
		fileName = searchTerm
	}

	// Save new sound
	newSound, err := storage.SaveSound(ctx.Session, ctx, fileName, reader)
	if err != nil {
		return nil, err
	}

	// Update metadata
	if oldMetadata != "" {
		if err := storage.UpdateSoundMetadata(ctx.Session, ctx, newSound, oldMetadata); err != nil {
			log.Printf("Warning: could not update metadata: %v", err)
		}
	}

	// Delete old sound
	if err := storage.DeleteSound(ctx.Session, ctx, sound); err != nil {
		log.Printf("Warning: could not delete old sound: %v", err)
	}

	newSound.Volume = sound.Volume

	return newSound, nil
}

// Validate storage backend is working
func ValidateStorage(storage StorageBackend) error {
	if storage == nil {
		return errors.New("storage backend is nil")
	}
	
	switch s := storage.(type) {
	case *VolumeStorage:
		// Check if base path exists or can be created
		if err := os.MkdirAll(s.BasePath, 0755); err != nil {
			return fmt.Errorf("cannot create volume storage directory: %w", err)
		}
		log.Printf("Volume storage validated at: %s", s.BasePath)
	case *DiscordStorage:
		log.Println("Discord storage selected (no validation needed)")
	default:
		return fmt.Errorf("unknown storage backend type: %T", storage)
	}
	
	return nil
}
