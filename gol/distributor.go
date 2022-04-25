package gol

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
	"uk.ac.bris.cs/gameoflife/util"
)

type distributorChannels struct {
	events     chan<- Event
	ioCommand  chan<- ioCommand
	ioIdle     <-chan bool
	ioFilename chan<- string
	ioInput    <-chan byte
	ioOutput   chan<- byte
	keyPresses <-chan rune
}

type aliveCellsInfo struct {
	turns int
	alive int
}

type status struct {
	val string
	m   sync.Mutex
}

const isAlive = byte(255)
const isDead = byte(0)

var exitCode int

func (s *status) Get() string {
	s.m.Lock()
	defer s.m.Unlock()
	return s.val
}

func (s *status) Set(val string) {
	s.m.Lock()
	defer s.m.Unlock()
	s.val = val
}

// handleStates handles the various key presses, a ticker timer and triggers GUI output when paused/resumed
func handleStates(p Params, c distributorChannels, world [][]byte, currentState aliveCellsInfo,
	ticker time.Ticker, play chan<- bool, stateBlock chan<- string, status *status) {

	for {
		select {
		case <-ticker.C:
			if status.Get() == "Play" {
				//Potentially fixed race condition
				state := currentState
				c.events <- AliveCellsCount{state.turns, state.alive}
				stateBlock <- "play"
			} else if status.Get() == "Pause" {
				stateBlock <- "pause"
			}
			return
		case key := <-c.keyPresses:
			switch key {
			case 's':
				exitCode = 0
				saveOutput(p, c, world, currentState.turns)
				if status.Get() == "Pause" {
					stateBlock <- "pause"
				} else if status.Get() == "Play" {
					stateBlock <- "play"
				}
				return
			case 'q':
				stateBlock <- "exit"
				return
			case 'p':
				go changePauseState(c, currentState.turns, play, stateBlock, status)
			}
		default:
			if status.Get() == "Pause" {
				//user has paused
				unpause := <-c.keyPresses
				if unpause == 'p' {
					go changePauseState(c, currentState.turns, play, stateBlock, status)

				} else if unpause == 'q' {
					play <- true
					stateBlock <- "exit"

				} else if unpause == 's' {
					saveOutput(p, c, world, currentState.turns)
					stateBlock <- "pause"
				}
			} else {
				stateBlock <- "play"
			}
			return
		}
	}
}

func changePauseState(c distributorChannels, turn int, play chan<- bool, stateBlock chan<- string,
	status *status) {
	if status.Get() == "Play" {
		c.events <- StateChange{turn, Paused}
		status.Set("Pause")
		stateBlock <- "pause"

	} else if status.Get() == "Pause" {
		fmt.Println("Continuing")
		c.events <- StateChange{turn, Executing}
		status.Set("Play")
		play <- true
		stateBlock <- "play"
	}
}

func saveOutput(p Params, c distributorChannels, world [][]byte, turn int) {
	doneOutputWorld := make(chan bool)
	//send output final image
	c.ioCommand <- ioOutput
	c.ioFilename <- strconv.Itoa(p.ImageWidth) + "x" + strconv.Itoa(p.ImageHeight) + "x" + strconv.Itoa(turn)
	go outputWorld(world, p, c, doneOutputWorld)
	<-doneOutputWorld
	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	c.events <- ImageOutputComplete{
		CompletedTurns: turn,
		Filename:       strconv.Itoa(p.ImageWidth) + "x" + strconv.Itoa(p.ImageHeight) + "x" + strconv.Itoa(turn),
	}
}

// outputWorld sends byte values of world to c.ioOutput channel for saving to folder /out
func outputWorld(world [][]byte, p Params, c distributorChannels, doneOutputWorld chan<- bool) {

	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			c.ioOutput <- world[y][x]
		}
	}
	doneOutputWorld <- true
}

func quitExecution(p Params, c distributorChannels, world [][]byte, turn int) {
	//send output final image
	doneOutputWorld := make(chan bool)
	c.ioCommand <- ioOutput
	c.ioFilename <- strconv.Itoa(p.ImageWidth) + "x" + strconv.Itoa(p.ImageHeight) + "x" + strconv.Itoa(turn)
	go outputWorld(world, p, c, doneOutputWorld)
	<-doneOutputWorld
	quitCells := calculateAliveCells(p, world)
	c.events <- FinalTurnComplete{CompletedTurns: turn, Alive: quitCells}

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	c.events <- ImageOutputComplete{
		CompletedTurns: turn,
		Filename:       strconv.Itoa(p.ImageWidth) + "x" + strconv.Itoa(p.ImageHeight) + "x" + strconv.Itoa(turn),
	}

	c.events <- StateChange{turn, Quitting}

	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)

	if c.keyPresses != nil {
		//give time for quitting event to be printed
		time.Sleep(2 * time.Second)
		os.Exit(exitCode)
	}
}

func calculateAliveCells(p Params, world [][]byte) []util.Cell {
	var aliveCells []util.Cell

	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			if world[y][x] == isAlive {
				aliveCells = append(aliveCells, util.Cell{X: x, Y: y})
			}
		}
	}

	return aliveCells
}

// mod deal with -1 and p.ImageHeight indexes
func mod(x, m int) int {
	return (x + m) % m
}

func calculateNeighbours(p Params, x, y int, world [][]byte) int {
	neighbours := 0

	for i := -1; i <= 1; i++ {
		for j := -1; j <= 1; j++ {
			if i != 0 || j != 0 {
				if world[mod(y+i, p.ImageHeight)][mod(x+j, p.ImageWidth)] == isAlive {
					neighbours++
				}
			}
		}
	}

	return neighbours
}

func calculateNextState(p Params, c distributorChannels, world [][]byte, startX int,
	startY int, endX int, endY int, start chan [][]byte,
	returnChan chan<- string) {
	for k := 0; k < p.Turns; k++ {
		newWorld := <-start
		for y := startY; y < endY; y++ {
			for x := startX; x < endX; x++ {
				neighbours := calculateNeighbours(p, x, y, world)
				switch neighbours {
				case 3:
					newWorld[y][x] = isAlive

					if world[y][x] == isDead {
						c.events <- CellFlipped{CompletedTurns: k + 1, Cell: util.Cell{X: x, Y: y}}
					}
				case 2:
					newWorld[y][x] = world[y][x]
				default:
					newWorld[y][x] = isDead

					if world[y][x] == isAlive {
						c.events <- CellFlipped{CompletedTurns: k + 1, Cell: util.Cell{X: x, Y: y}}
					}
				}
			}
		}
		returnChan <- "done"
	}
}

func gameOfLife(p Params, c distributorChannels, initialWorld [][]byte) {
	status := &status{}
	currentState := aliveCellsInfo{turns: 0, alive: 0}
	world := initialWorld
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	go func() {
		status.Set("Play")
	}()

	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			if world[y][x] == isAlive {
				c.events <- CellFlipped{CompletedTurns: 0, Cell: util.Cell{X: x, Y: y}}
			}
		}
	}

	newWorld := make([][]byte, p.ImageHeight)
	returnChan := make(chan string)
	startChan := make(chan [][]byte)
	for t := 0; t < p.Threads; t++ {
		go calculateNextState(p, c, world, 0, t*p.ImageHeight/p.Threads, p.ImageWidth, (t+1)*p.ImageHeight/p.Threads, startChan, returnChan)
	}
	play := make(chan bool)
	stateBlock := make(chan string)

	for turn := 0; turn < p.Turns; turn++ {
		for i := range newWorld {
			newWorld[i] = make([]byte, p.ImageWidth)
		}
		for t := 0; t < p.Threads; t++ {
			startChan <- newWorld
		}
		for t := 0; t < p.Threads; t++ {
			<-returnChan
		}

		copy(world, newWorld)

		currentState.turns = turn + 1
		currentState.alive = len(calculateAliveCells(p, world))
		c.events <- TurnComplete{CompletedTurns: turn + 1}
		go handleStates(p, c, world, currentState, *ticker, play, stateBlock, status)
		state := <-stateBlock
		if state == "pause" {
			<-play
		} else if state == "exit" {
			break
		}
	}

	exitCode = 0
	if currentState.turns != p.Turns {
		exitCode = 1
	}
	quitExecution(p, c, world, currentState.turns)
}

func initialiseWorld(p Params, c distributorChannels) [][]byte {
	initialWorld := make([][]byte, p.ImageHeight)

	for i := 0; i < p.ImageWidth; i++ {
		initialWorld[i] = make([]byte, p.ImageWidth)
	}

	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			initialWorld[y][x] = <-c.ioInput
		}
	}
	c.events <- AliveCellsCount{CompletedTurns: 0, CellsCount: 0}
	return initialWorld
}

// distributor divides the work between workers and interacts with other goroutines.
func distributor(p Params, c distributorChannels) {
	exitCode = -1
	c.ioCommand <- ioInput
	c.ioFilename <- strconv.Itoa(p.ImageWidth) + "x" + strconv.Itoa(p.ImageHeight)
	initialWorld := initialiseWorld(p, c)

	gameOfLife(p, c, initialWorld)
}
