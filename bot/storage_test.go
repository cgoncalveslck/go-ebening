package bot

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestVolumeStorageInit(t *testing.T) {
	tmpDir := t.TempDir()
	
	vs := &VolumeStorage{BasePath: tmpDir}
	
	if err := ValidateStorage(vs); err != nil {
		t.Fatalf("ValidateStorage failed: %v", err)
	}
	
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		t.Errorf("Base path was not created: %v", err)
	}
}

func TestVolumeStorageSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	vs := &VolumeStorage{BasePath: tmpDir}
	
	guildID := "test-guild-123"
	soundName := "test-sound"
	soundData := []byte("fake mp3 data")
	
	// Create a fake context
	ctx := &Context{
		GuildID: guildID,
	}
	
	// Test saving a sound
	reader := bytes.NewReader(soundData)
	sound, err := vs.SaveSound(nil, ctx, soundName, reader)
	if err != nil {
		t.Fatalf("SaveSound failed: %v", err)
	}
	
	if sound.MessageID != soundName {
		t.Errorf("Expected MessageID to be %s, got %s", soundName, sound.MessageID)
	}
	
	expectedPath := filepath.Join(tmpDir, guildID, soundName+".mp3")
	if sound.URL != expectedPath {
		t.Errorf("Expected URL to be %s, got %s", expectedPath, sound.URL)
	}
	
	// Verify file was created
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("Sound file was not created at %s", expectedPath)
	}
	
	// Verify file contents
	savedData, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Failed to read saved file: %v", err)
	}
	if !bytes.Equal(savedData, soundData) {
		t.Errorf("Saved data doesn't match original data")
	}
	
	// Test loading sounds
	soundList, entrances, err := vs.LoadSounds(nil, guildID, "")
	if err != nil {
		t.Fatalf("LoadSounds failed: %v", err)
	}
	
	if len(soundList) != 1 {
		t.Errorf("Expected 1 sound, got %d", len(soundList))
	}
	
	loadedSound, ok := soundList[soundName]
	if !ok {
		t.Errorf("Sound %s not found in loaded sound list", soundName)
	}
	
	if loadedSound.URL != expectedPath {
		t.Errorf("Loaded sound URL doesn't match: expected %s, got %s", expectedPath, loadedSound.URL)
	}
	
	if len(entrances) != 0 {
		t.Errorf("Expected 0 entrances, got %d", len(entrances))
	}
}

func TestVolumeStorageMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	vs := &VolumeStorage{BasePath: tmpDir}
	
	guildID := "test-guild-456"
	soundName := "test-sound-meta"
	
	ctx := &Context{
		GuildID: guildID,
	}
	
	// Save a sound
	soundData := []byte("test data")
	sound, err := vs.SaveSound(nil, ctx, soundName, bytes.NewReader(soundData))
	if err != nil {
		t.Fatalf("SaveSound failed: %v", err)
	}
	
	// Test updating metadata
	metadata := "v:256;e:user123;"
	err = vs.UpdateSoundMetadata(nil, ctx, sound, metadata)
	if err != nil {
		t.Fatalf("UpdateSoundMetadata failed: %v", err)
	}
	
	// Test reading metadata
	readMetadata, err := vs.GetSoundMetadata(nil, ctx, sound)
	if err != nil {
		t.Fatalf("GetSoundMetadata failed: %v", err)
	}
	
	if readMetadata != metadata {
		t.Errorf("Metadata mismatch: expected %s, got %s", metadata, readMetadata)
	}
	
	// Verify metadata file exists
	metadataPath := sound.URL + ".meta"
	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		t.Errorf("Metadata file was not created at %s", metadataPath)
	}
}

func TestVolumeStorageLoadWithMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	vs := &VolumeStorage{BasePath: tmpDir}
	
	guildID := "test-guild-789"
	soundName := "entrance-sound"
	userID := "user456"
	
	ctx := &Context{
		GuildID: guildID,
	}
	
	// Save a sound with metadata
	soundData := []byte("entrance music data")
	sound, err := vs.SaveSound(nil, ctx, soundName, bytes.NewReader(soundData))
	if err != nil {
		t.Fatalf("SaveSound failed: %v", err)
	}
	
	metadata := "e:" + userID + ";v:200;"
	err = vs.UpdateSoundMetadata(nil, ctx, sound, metadata)
	if err != nil {
		t.Fatalf("UpdateSoundMetadata failed: %v", err)
	}
	
	// Load sounds and verify entrance is parsed
	soundList, entrances, err := vs.LoadSounds(nil, guildID, "")
	if err != nil {
		t.Fatalf("LoadSounds failed: %v", err)
	}
	
	if len(soundList) != 1 {
		t.Errorf("Expected 1 sound, got %d", len(soundList))
	}
	
	loadedSound := soundList[soundName]
	if loadedSound.Volume != 200 {
		t.Errorf("Expected volume 200, got %d", loadedSound.Volume)
	}
	
	if len(entrances) != 1 {
		t.Errorf("Expected 1 entrance, got %d", len(entrances))
	}
	
	entranceSound, ok := entrances[userID]
	if !ok {
		t.Errorf("Entrance for user %s not found", userID)
	}
	
	if entranceSound.MessageID != soundName {
		t.Errorf("Entrance sound mismatch: expected %s, got %s", soundName, entranceSound.MessageID)
	}
}

func TestVolumeStorageDelete(t *testing.T) {
	tmpDir := t.TempDir()
	vs := &VolumeStorage{BasePath: tmpDir}
	
	guildID := "test-guild-delete"
	soundName := "sound-to-delete"
	
	ctx := &Context{
		GuildID: guildID,
	}
	
	// Save a sound with metadata
	soundData := []byte("data to delete")
	sound, err := vs.SaveSound(nil, ctx, soundName, bytes.NewReader(soundData))
	if err != nil {
		t.Fatalf("SaveSound failed: %v", err)
	}
	
	metadata := "v:100;"
	err = vs.UpdateSoundMetadata(nil, ctx, sound, metadata)
	if err != nil {
		t.Fatalf("UpdateSoundMetadata failed: %v", err)
	}
	
	// Verify files exist
	if _, err := os.Stat(sound.URL); os.IsNotExist(err) {
		t.Errorf("Sound file doesn't exist before delete")
	}
	metadataPath := sound.URL + ".meta"
	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		t.Errorf("Metadata file doesn't exist before delete")
	}
	
	// Delete the sound
	err = vs.DeleteSound(nil, ctx, sound)
	if err != nil {
		t.Fatalf("DeleteSound failed: %v", err)
	}
	
	// Verify files are deleted
	if _, err := os.Stat(sound.URL); !os.IsNotExist(err) {
		t.Errorf("Sound file still exists after delete")
	}
	if _, err := os.Stat(metadataPath); !os.IsNotExist(err) {
		t.Errorf("Metadata file still exists after delete")
	}
}

func TestNewStorageBackend(t *testing.T) {
	// Test default (Discord storage)
	os.Unsetenv("STORAGE_MODE")
	backend := NewStorageBackend()
	if _, ok := backend.(*DiscordStorage); !ok {
		t.Errorf("Expected DiscordStorage as default, got %T", backend)
	}
	
	// Test volume storage
	os.Setenv("STORAGE_MODE", "volume")
	tmpDir := t.TempDir()
	os.Setenv("VOLUME_PATH", tmpDir)
	defer os.Unsetenv("STORAGE_MODE")
	defer os.Unsetenv("VOLUME_PATH")
	
	backend = NewStorageBackend()
	vs, ok := backend.(*VolumeStorage)
	if !ok {
		t.Errorf("Expected VolumeStorage with STORAGE_MODE=volume, got %T", backend)
	}
	
	if vs.BasePath != tmpDir {
		t.Errorf("Expected BasePath to be %s, got %s", tmpDir, vs.BasePath)
	}
}

func TestReuploadSoundWithStorage(t *testing.T) {
	tmpDir := t.TempDir()
	vs := &VolumeStorage{BasePath: tmpDir}
	
	guildID := "test-guild-reupload"
	oldSoundName := "old-sound"
	newSoundName := "new-sound"
	userID := "user789"
	
	ctx := &Context{
		GuildID: guildID,
		State: &State{
			Entrances: make(Entrances),
		},
	}
	
	// Create initial sound with metadata
	soundData := []byte("sound data for reupload")
	oldSound, err := vs.SaveSound(nil, ctx, oldSoundName, bytes.NewReader(soundData))
	if err != nil {
		t.Fatalf("SaveSound failed: %v", err)
	}
	
	oldMetadata := "e:" + userID + ";v:150;"
	err = vs.UpdateSoundMetadata(nil, ctx, oldSound, oldMetadata)
	if err != nil {
		t.Fatalf("UpdateSoundMetadata failed: %v", err)
	}
	
	// Reupload with new name
	newSound, err := reuploadSoundWithStorage(vs, ctx, oldSound, oldSoundName, newSoundName)
	if err != nil {
		t.Fatalf("reuploadSoundWithStorage failed: %v", err)
	}
	
	// Verify new sound exists
	newSoundPath := filepath.Join(tmpDir, guildID, newSoundName+".mp3")
	if newSound.URL != newSoundPath {
		t.Errorf("New sound path mismatch: expected %s, got %s", newSoundPath, newSound.URL)
	}
	
	if _, err := os.Stat(newSoundPath); os.IsNotExist(err) {
		t.Errorf("New sound file was not created")
	}
	
	// Verify metadata was copied
	newMetadata, err := vs.GetSoundMetadata(nil, ctx, newSound)
	if err != nil {
		t.Fatalf("GetSoundMetadata failed: %v", err)
	}
	
	if newMetadata != oldMetadata {
		t.Errorf("Metadata not preserved: expected %s, got %s", oldMetadata, newMetadata)
	}
	
	// Verify old sound was deleted
	oldSoundPath := filepath.Join(tmpDir, guildID, oldSoundName+".mp3")
	if _, err := os.Stat(oldSoundPath); !os.IsNotExist(err) {
		t.Errorf("Old sound file was not deleted")
	}
}

func BenchmarkVolumeStorageSave(b *testing.B) {
	tmpDir := b.TempDir()
	vs := &VolumeStorage{BasePath: tmpDir}
	
	ctx := &Context{
		GuildID: "benchmark-guild",
	}
	
	soundData := bytes.Repeat([]byte("test"), 1024) // 4KB of data
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		soundName := "bench-sound"
		_, err := vs.SaveSound(nil, ctx, soundName, bytes.NewReader(soundData))
		if err != nil {
			b.Fatalf("SaveSound failed: %v", err)
		}
	}
}

func BenchmarkVolumeStorageLoad(b *testing.B) {
	tmpDir := b.TempDir()
	vs := &VolumeStorage{BasePath: tmpDir}
	
	guildID := "benchmark-guild"
	ctx := &Context{
		GuildID: guildID,
	}
	
	// Pre-populate with sounds
	soundData := bytes.Repeat([]byte("test"), 1024)
	for i := 0; i < 10; i++ {
		soundName := "sound-" + string(rune('0'+i))
		_, err := vs.SaveSound(nil, ctx, soundName, bytes.NewReader(soundData))
		if err != nil {
			b.Fatalf("SaveSound failed: %v", err)
		}
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := vs.LoadSounds(nil, guildID, "")
		if err != nil {
			b.Fatalf("LoadSounds failed: %v", err)
		}
	}
}
