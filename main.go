package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	guilds   = map[string]*guild{}
	waitTime = 1 * time.Minute
	token    = flag.String("token", "", "discord bot token")
)

type guild struct {
	id          string
	nextWord    map[string]struct{}
	around      string
	story       string
	timer       *time.Timer
	toDelete    []string
	toDeleteEnd []string
}

func main() {
	flag.Parse()
	dg, err := discordgo.New("Bot " + *token)
	if err != nil {
		log.Fatal(err)
	}

	dg.AddHandler(messageCreate)
	dg.AddHandler(messageDelete)

	dg.Identify.Intents = discordgo.IntentsGuildMessages

	// Open a websocket connection to Discord and begin listening.
	err = dg.Open()
	if err != nil {
		log.Fatalf("Error opening connection: %v", err)
	}

	// Wait here until CTRL-C or other term signal is received.
	fmt.Println("Bot is now running.  Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	<-sc

	// Cleanly close down the Discord session.
	dg.Close()
}

func (g *guild) count(s *discordgo.Session, channelID string, messages map[string]struct{}) map[string]int {
	var msgs = map[string]int{}
	newMessages := map[string]struct{}{}
	for k, v := range messages {
		newMessages[k] = v
	}

	cMsgs, err := s.ChannelMessages(channelID, 100, "", "", g.around)
	if err != nil {
		fmt.Println(err)
		return msgs
	}
	var after string
	for {
		for _, m := range cMsgs {
			if _, ok := newMessages[m.ID]; ok {
				msgs[m.Content] = msgs[m.Content] + len(m.Reactions)
				delete(newMessages, m.ID)
			}
		}
		if len(cMsgs) < 100 {
			return msgs
		}
		after = cMsgs[99].ID
		cMsgs, err = s.ChannelMessages(channelID, 100, "", after, "")
		if err != nil {
			fmt.Println(err)
			return msgs
		}
	}
}

func (g *guild) end(s *discordgo.Session, channelID string) {
	log.Printf("guild %q end called", g.id)

	for _, id := range g.toDeleteEnd {
		if err := s.ChannelMessageDelete(channelID, id); err != nil {
			log.Print(err)
		}
	}
	g.toDeleteEnd = nil

	s.ChannelMessageSend(channelID, fmt.Sprintf("%q", g.story))
	g.story = ""
	m, err := s.ChannelMessageSend(channelID, "First word?")
	if err != nil {
		log.Print(err)
		return
	}
	g.toDelete = append(g.toDelete, m.ID)
}

func deleteMessages(s *discordgo.Session, channelID string, messages []string) {
	for {
		s.ChannelMessagesBulkDelete(channelID, messages)
		if len(messages) > 100 {
			messages = messages[100:]
		} else {
			return
		}
	}
}

func (g *guild) choose(s *discordgo.Session, channelID string) {
	log.Printf("guild %q choose called", g.id)

	msgs := g.count(s, channelID, g.nextWord)
	var word string
	var high int
	for k, v := range msgs {
		if v >= high {
			word = strings.TrimSpace(k)
			high = v
		}
	}
	log.Printf("guild %q word is %q", g.id, word)
	if word == "" {
		return
	}

	// Delete all previous suggestions and cleanup bot messages.
	var deletes []string
	for k := range g.nextWord {
		deletes = append(deletes, k)
	}
	deletes = append(deletes, g.toDelete...)
	deleteMessages(s, channelID, deletes)
	g.toDelete = nil

	g.nextWord = map[string]struct{}{}
	if g.timer != nil {
		g.timer.Stop()
	}

	if g.story == "" {
		word = strings.Title(word)
	}
	g.story = fmt.Sprintf("%s%s ", g.story, word)
	lastChar := word[len(word)-1:]
	if lastChar == "." || lastChar == "!" || lastChar == "?" {
		g.end(s, channelID)
		return
	}

	m, err := s.ChannelMessageSend(channelID, fmt.Sprintf("Chosen word is %q with %d votes. Story so far:\n%s", word, high, g.story))
	if err != nil {
		log.Print(err)
		return
	}
	g.toDelete = append(g.toDelete, m.ID)

	m, err = s.ChannelMessageSend(channelID, "Next word?")
	if err != nil {
		log.Print(err)
		return
	}
	g.around = m.ID
	g.toDelete = append(g.toDelete, m.ID)
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}
	c, err := s.Channel(m.ChannelID)
	if err != nil {
		log.Print(err)
		return
	}
	if c.Name != "one-word-story" {
		return
	}

	log.Printf("guild %q messageCreate: %q", m.GuildID, m.Content)

	g, ok := guilds[m.GuildID]
	if !ok {
		log.Printf("new guild %q", m.GuildID)
		g = &guild{id: m.GuildID, nextWord: map[string]struct{}{}}
		guilds[m.GuildID] = g
	}
	log.Printf("%+v", g)

	if m.Message.Content == "!list" {
		msgs := g.count(s, m.ChannelID, g.nextWord)
		var msg string
		for text, cnt := range msgs {
			msg = fmt.Sprintf("%s%s:%d\n", msg, text, cnt)
		}
		s.ChannelMessageSend(m.ChannelID, msg)
		return
	}

	if m.Message.Content == "!choose" {
		g.toDelete = append(g.toDelete, m.ID)
		g.choose(s, m.ChannelID)
		return
	}

	if m.Message.Content == "!end" {
		g.toDeleteEnd = append(g.toDeleteEnd, m.ID)
		g.end(s, m.ChannelID)
		return
	}

	if len(g.nextWord) == 0 {
		g.timer = time.AfterFunc(waitTime, func() { g.choose(s, m.ChannelID) })
	}
	g.nextWord[m.ID] = struct{}{}
}

func messageDelete(s *discordgo.Session, m *discordgo.MessageDelete) {
	c, err := s.Channel(m.ChannelID)
	if err != nil {
		log.Print(err)
		return
	}
	if c.Name != "one-word-story" {
		return
	}

	log.Printf("guild %q messageDelete: %q", m.GuildID, m.Content)

	g, ok := guilds[m.GuildID]
	if !ok {
		return
	}
	delete(g.nextWord, m.Message.ID)
}
