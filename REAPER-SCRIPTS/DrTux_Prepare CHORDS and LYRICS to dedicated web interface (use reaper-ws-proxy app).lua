--[[
 * ReaScript Name: Prepare Chords and Lyrics to dedicated web interface
 * Author: Karamush (also DrTux)
 * Author URI: https://karamush.ru/
 * Repository: GitHub > karamush > REAPER-ReaScripts
 * Repository URI: -- TODO
 * Licence: GPL v3
 * REAPER: 5.0
 * Link: TODO
 * Version: 1.0
--]]

--[[
 * Changelog:
 * v1.0 (2025-11-25)
  + Initial Release
--]]


EXT_NAME = "TUX"
EXT_KEYS_TO_CLEAR = {"text", "next", "need_refresh", "json", "chords_json"}

LYRICS_STR_NO_TEXT = "--XR-NO-TEXT--"

function Msg(val)
    reaper.ShowConsoleMsg(tostring(val) .. "\n")
end

function MsgBox(text, title, type)
    reaper.ShowMessageBox(tostring(text), title or "Info", type or 0)
end

function GetTrackByName(trackName)
    trackName = trackName:lower()
    local trackByName = nil
    local count_tracks = reaper.CountTracks(0)
    for i = 0, count_tracks - 1 do
        local track = reaper.GetTrack(0, i)
        local retval, track_name = reaper.GetTrackName(track, "")
        if retval and track_name:lower() == trackName then
            trackByName = track
            break
        end
    end
    return trackByName
end

function GetChordsTrack()
    return GetTrackByName("chords")
end

function GetLyricsTrack()
    return GetTrackByName("lyrics")
end

local function escape_json_str(s)
    if not s then
        return ""
    end
    s = tostring(s)
    s = s:gsub("\\", "\\\\")
    s = s:gsub('"', '\\"')
    s = s:gsub("\n", "<br>")
    s = s:gsub("\r", "")
    return s
end

function GetTrackItemAtPos(track, pos)
    local count_track_items = reaper.GetTrackNumMediaItems(track)
    local current_item
    for i = 0, count_track_items - 1 do
        local item = reaper.GetTrackMediaItem(track, i)
        local item_pos = reaper.GetMediaItemInfo_Value(item, "D_POSITION")
        if item_pos <= pos then -- if item is after cursor then ignore
            local item_len = reaper.GetMediaItemInfo_Value(item, "D_LENGTH")
            if item_pos + item_len > pos then -- if item end is after cursor, then item is under cusor
                current_item = item
                break
            end
        end
    end
    return current_item
end

function ProcessItemNotes(item, ext_key, text)
    if not item then
        reaper.SetExtState(EXT_NAME, ext_key, LYRICS_STR_NO_TEXT, false)
        return nil
    end
    local item_notes = reaper.ULT_GetMediaItemNote(item):gsub("\r?\n", "<br>")
    if item_notes ~= text then
        text = item_notes == "" and LYRICS_STR_NO_TEXT or item_notes
        reaper.SetExtState(EXT_NAME, ext_key, text, false)
    end
    return text
end

-- Set ToolBar Button State
function SetButtonState(set)
    if not set then
        set = 0
    end
    local is_new_value, filename, sec, cmd, mode, resolution, val = reaper.get_action_context()
    local state = reaper.GetToggleCommandStateEx(sec, cmd)
    reaper.SetToggleCommandState(sec, cmd, set) -- Set ON
    reaper.RefreshToolbar2(sec, cmd)
end

function Exit()
    for i, k in ipairs(EXT_KEYS_TO_CLEAR) do
        reaper.SetExtState(EXT_NAME, k, "", false)
    end
    SetButtonState()
end

function MakeChords()
    local track = GetChordsTrack()
    if not track then
        Msg("No Chords track! Skips!")
        return false
    end

    local count_track_items = reaper.CountTrackMediaItems(track)
    if count_track_items == 0 then
        Msg("No items in Chords track! Skips!")
        return false
    end

    local proj = 0
    local t = {}

    for i = 0, count_track_items - 1 do
        local item = reaper.GetTrackMediaItem(track, i)
        local retval, item_notes = reaper.GetSetMediaItemInfo_String(item, "P_NOTES", "", false)
        if retval and item_notes ~= "" then
            local item_pos = reaper.GetMediaItemInfo_Value(item, "D_POSITION")
            local item_len = reaper.GetMediaItemInfo_Value(item, "D_LENGTH")
            local item_end = item_pos + item_len

            -- Получаем текущий timesig и bpm в точке start
            local ts_num, ts_denom, bpm = reaper.TimeMap_GetTimeSigAtTime(proj, item_pos)
            -- Если TimeMap_GetTimeSigAtTime вернул nil/0 для bpm — защитимся
            bpm = tonumber(bpm) or 0
            ts_denom = tonumber(ts_denom) or 4

            -- Вычисляем количество долей (beats) в единицах четверти (BPM — квартовые доли в минуту)
            -- beats_float = длина (сек) * bpm / 60
            local beats_float = 0
            if bpm > 0 then
                beats_float = (item_len * bpm) / 60.0
            end

            -- Переводим в единицы текущего знаменателя (например, если denom=8 — считаем восьмушки)
            -- units_per_quarter = ts_denom / 4
            local units_per_quarter = ts_denom / 4
            local units_exact = beats_float * units_per_quarter
            local units_round = math.floor(units_exact + 0.5) -- округление до ближайшего "юнита" указанного знаменателя

            local timesig_like = tostring(units_round) .. "/" .. tostring(ts_denom)

            table.insert(
                t,
                {
                    text = item_notes,
                    pos_start = item_pos,
                    pos_end = item_end,
                    bpm = bpm,
                    beats = beats_float,
                    units_exact = units_exact,
                    units_round = units_round,
                    unit_denom = ts_denom,
                    timesig_like = timesig_like
                }
            )
        end
    end

    -- Формируем JSON
    local lines = {'{ "entry": ['}
    for _, v in ipairs(t) do
        local safe_text = escape_json_str(v.text)
        local line =
            string.format(
            '\n{\n  "pos_start": %.6f,\n  "pos_end": %.6f,\n  "text": "%s",\n  "bpm": %.2f,\n  "beats": %.4f,\n "timesig_like": "%s"\n},',
            v.pos_start,
            v.pos_end,
            safe_text,
            v.bpm,
            v.beats,
            -- v.units_exact,
            -- v.units_round,
            -- v.unit_denom,
            v.timesig_like
        )
        table.insert(lines, line)
    end
    table.insert(lines, "]\n}")

    local json = table.concat(lines):gsub(",%s*%]", "]") -- убрать лишнюю запятую перед ]
    reaper.ClearConsole()
    -- reaper.ShowConsoleMsg(json)
    reaper.SetExtState(EXT_NAME, "chords_json", json, false)
    reaper.SetExtState(EXT_NAME, "need_refresh", "true", false)
end

function MakeLyrics()
    local count_track_items = reaper.CountTrackMediaItems(LyricsTrack)
    if count_track_items == 0 then
        Msg("No items in Lyrics track! Skips!")
        return false
    end

    local t = {}
    for i = 0, count_track_items - 1 do
        local item = reaper.GetTrackMediaItem(LyricsTrack, i)
        local retval, item_notes = reaper.GetSetMediaItemInfo_String(item, "P_NOTES", "", false)
        if retval and item_notes ~= "" then
            local item_pos = reaper.GetMediaItemInfo_Value(item, "D_POSITION")
            local item_len = reaper.GetMediaItemInfo_Value(item, "D_LENGTH")
            local item_end = item_pos + item_len
            table.insert(t, {text = item_notes, pos_start = item_pos, pos_end = item_end})
        end
    end

    local lines = {'{ "entry": ['}
    for i, v in ipairs(t) do
        local line =
            '\n{\n  "pos_start": ' ..
            v.pos_start ..
                ',\n  "pos_end": ' ..
                    v.pos_end .. ',\n  "text": "' .. v.text:gsub("\n", "<br>"):gsub("\r", "") .. '"\n},'
        table.insert(lines, line)
    end
    table.insert(lines, "]\n}")

    local json = table.concat(lines):gsub(",]", "]")
    -- reaper.ClearConsole()
    reaper.SetExtState(EXT_NAME, "json", json, false)
    reaper.SetExtState(EXT_NAME, "need_refresh", "true", false)
end

function ProcessCurrentLyrics()
    -- Get play or edit cursor
    local cur_pos = reaper.GetPlayState() > 0 and reaper.GetPlayPosition() or reaper.GetCursorPosition()
    local notes = nil

    if reaper.ValidatePtr(LyricsTrack, "MediaTrack*") then
        local item = GetTrackItemAtPos(LyricsTrack, cur_pos)
        notes = ProcessItemNotes(item, "text", notes)
    else
        LyricsTrack = GetLyricsTrack()
    end

    reaper.defer(ProcessCurrentLyrics)
end

-- RUN
-- Сначала получаем LyricsTrack, т.к. используется он в двух функциях
LyricsTrack = GetLyricsTrack()

MakeChords()
if LyricsTrack then
    MakeLyrics()
end

if LyricsTrack then
    SetButtonState(1)
    ProcessCurrentLyrics()
    reaper.atexit(Exit)
else
    reaper.MB('No tracks named "Lyrics".', "Error", 0)
end
