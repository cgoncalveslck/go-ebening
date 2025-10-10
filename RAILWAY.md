# Railway Configuration Example

## Using Discord Channel Storage (Default)

This is the simplest setup and requires no volumes:

### Environment Variables:
```
BOT_TOKEN=your_discord_bot_token_here
```

## Using Railway Volume Storage (Recommended)

This setup uses Railway volumes for persistent file storage:

### Steps:

1. **Create a Volume in Railway:**
   - Go to your Railway project
   - Click on your service
   - Go to the "Volumes" tab
   - Click "New Volume"
   - Name it something like "sounds-volume"
   - Set the mount path to: `/data/sounds`

2. **Set Environment Variables:**
```
BOT_TOKEN=your_discord_bot_token_here
STORAGE_MODE=volume
VOLUME_PATH=/data/sounds
```

3. **Deploy:**
   - Your bot will now store audio files on the persistent volume
   - Files will persist across deployments and restarts
   - No Discord rate limits for file storage

## Benefits of Volume Storage

- **Better Performance**: Files are stored locally instead of being downloaded from Discord CDN each time
- **No Discord Rate Limits**: Avoid Discord's rate limits on file uploads/downloads
- **Persistence**: Files remain even if Discord messages are deleted
- **Scalability**: Can store more files without worrying about Discord channel limits
- **Faster Loading**: Bot starts faster as it reads from local disk instead of fetching from Discord

## Migration from Discord to Volume Storage

Both storage modes can coexist, allowing for seamless rollback if needed:

1. Deploy with `STORAGE_MODE=volume`
2. Re-upload sounds (they'll be saved to volume)
3. If issues occur, set `STORAGE_MODE=discord` to rollback
4. Old sounds in Discord remain accessible

## Volume Size Recommendations

- **Small bot** (< 100 sounds): 1 GB
- **Medium bot** (100-500 sounds): 2-5 GB  
- **Large bot** (> 500 sounds): 10+ GB

MP3 files are typically 1-5 MB each, so plan accordingly.
