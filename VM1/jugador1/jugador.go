package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	pb "jugador/proto/grpc-server/proto"
	"google.golang.org/grpc"
)

// Constantes para la configuración del sistema
const (
	// Suponemos un sistema con 1 Matchmaker, 2 Jugadores y 3 Servidores. Total 6 entidades.
	// Orden: [Jugador1, Jugador2, Matchmaker, Servidor1, Servidor2, Servidor3]
	totalEntities = 6
)

// Estado del cliente jugador
type playerClient struct {
	id          string
	playerIndex int // El índice de este jugador en el reloj vectorial (0 para Jugador1, 1 para Jugador2)
	vectorClock []int32
	grpcClient  pb.MatchmakerClient
	mu          sync.Mutex // Mutex para proteger el acceso al reloj vectorial
}

// Función para fusionar relojes vectoriales.
func (c *playerClient) mergeClocks(remoteClock []int32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.vectorClock) != len(remoteClock) {
		return // No se pueden fusionar relojes de diferente tamaño
	}
	for i := 0; i < len(c.vectorClock); i++ {
		if remoteClock[i] > c.vectorClock[i] {
			c.vectorClock[i] = remoteClock[i]
		}
	}
	log.Printf("[Player %s] Reloj fusionado: %v", c.id, c.vectorClock)
}

// Función para incrementar el reloj vectorial local antes de un evento.
func (c *playerClient) incrementClock() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vectorClock[c.playerIndex]++
	log.Printf("[Player %s] Reloj incrementado: %v", c.id, c.vectorClock)
}

// ---- Implementación de Responsabilidades Principales ----

// 1. Permitir al usuario solicitar unirse a una cola
func (c *playerClient) joinQueue() {
	log.Printf("[Player %s] Intentando unirse a la cola...", c.id)

	c.incrementClock() // Incrementa el reloj antes de enviar la solicitud de escritura

	c.mu.Lock()
	req := &pb.QueuePlayerRequest{
		PlayerId:           c.id,
		GameModePreference: "1v1",
		VectorClock:        c.vectorClock,
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	res, err := c.grpcClient.QueuePlayer(ctx, req)
	if err != nil {
		log.Printf("Error al unirse a la cola: %v", err)
		return
	}

	// Al recibir la respuesta, fusiona el reloj del Matchmaker
	c.mergeClocks(res.GetVectorClock())
	log.Printf("Respuesta del MatchMaker: %s", res.GetMessage())
}

// 2. Permitir consultar el estado actual del jugador (Implementando Read-Your-Writes)
func (c *playerClient) checkStatus() {
	log.Printf("[Player %s] Consultando estado...", c.id)

	// Para RYW, no incrementamos el reloj, solo lo enviamos para que el servidor
	// sepa "qué tan actualizado" está nuestro conocimiento del sistema.
	c.mu.Lock()
	req := &pb.PlayerStatusRequest{
		PlayerId:    c.id,
		VectorClock: c.vectorClock,
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	res, err := c.grpcClient.GetPlayerStatus(ctx, req)
	if err != nil {
		log.Printf("Error al consultar estado: %v", err)
		return
	}

	// Al recibir la respuesta, también fusionamos el reloj para mantenernos actualizados.
	c.mergeClocks(res.GetVectorClock())
	log.Printf("Estado actual: %s", res.GetPlayerStatus())
	if res.GetPlayerStatus() == "IN_MATCH" {
		log.Printf("  -> Partida: %s en Servidor: %s", res.GetMatchId(), res.GetGameServerAddress())
	}
}

func (c *playerClient) leaveQueue() {
    log.Printf("[Player %s] Saliendo de la cola...", c.id)

    c.incrementClock() // Es una acción de escritura, incrementamos el reloj.

    c.mu.Lock()
    req := &pb.PlayerStatusRequest{
        PlayerId:    c.id,
        VectorClock: c.vectorClock,
    }
    c.mu.Unlock()

    ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
    defer cancel()

    res, err := c.grpcClient.LeaveQueue(ctx, req)
    if err != nil {
        log.Printf("Error al salir de la cola: %v", err)
        return
    }

    // Al recibir la respuesta, fusiona el reloj del Matchmaker
    c.mergeClocks(res.GetVectorClock())
}

// 3. Recibir mensajes del Matchmaker cuando una partida ha sido asignada
func (c *playerClient) listenForMatchAssignments() {
	log.Printf("[Player %s] Escuchando notificaciones de partida...", c.id)

	req := &pb.PlayerIdRequest{PlayerId: c.id}

	// El contexto para el stream debe ser de larga duración
	stream, err := c.grpcClient.StreamMatchNotifications(context.Background(), req)
	if err != nil {
		log.Fatalf("No se pudo abrir el stream de notificaciones: %v", err)
	}

	for {
		notification, err := stream.Recv()
		if err == io.EOF {
			log.Println("Stream de notificaciones cerrado por el servidor.")
			return
		}
		if err != nil {
			log.Printf("Error al recibir notificación: %v.", err)
			return
		}

		// ¡Partida encontrada!
		log.Println("==============================================")
		log.Printf("¡PARTIDA ENCONTRADA PARA EL JUGADOR %s!", c.id)
		log.Printf("  -> ID de Partida: %s", notification.GetMatchId())
		log.Printf("  -> Jugadores: %v", notification.GetPlayerIds())
		log.Printf("  -> Conectado a: %s", notification.GetGameServerAddress())
		log.Println("==============================================")

		// Actualizamos nuestro conocimiento del sistema con el reloj del evento de asignación
		c.mergeClocks(notification.GetVectorClock())
	}
}

func main() {
	log.Println("Iniciado cliente jugador...")
	// Conexión gRPC al Matchmaker Central ---- La dirección podría venir de una variable de entorno
	matchmakerAddr := "10.35.168.122:50051"
	conn, err := grpc.Dial(matchmakerAddr, grpc.WithInsecure())
	if err != nil {
		log.Fatalf("No se pudo conectar a MatchMaker: %v", err)
	}
	defer conn.Close()

	// id del jugador
	playerId := "1"
	playerIndex := 0 // primer jugador en el reloj vectorial

	client := &playerClient{
		id:          playerId,
		playerIndex: playerIndex,
		vectorClock: make([]int32, totalEntities), // Inicializa en [0, 0, 0, 0, 0, 0]
		grpcClient:  pb.NewMatchmakerClient(conn),
	}

	// Inicia la escucha de notificaciones en una goroutine para no bloquear el menú principal
	go func() {
		client.listenForMatchAssignments()
	}()

	// Menú interactivo para el usuario
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Println("\nSeleccione una opción:")
		fmt.Println("1. Unirse a la cola de emparejamiento")
		fmt.Println("2. Consultar mi estado")
		fmt.Println("3. Cancelar emparejamiento")
		fmt.Println("4. Salir")
		fmt.Print("> ")

		if !scanner.Scan() {
			break
		}
		choice := strings.TrimSpace(scanner.Text())

		switch choice {
		case "1":
			client.joinQueue()
		case "2":
			client.checkStatus()
		case "3":
			client.leaveQueue() 
		case "4":
			fmt.Println("¡Hasta luego!")
			return
		default:
			fmt.Println("Opción no válida.")
		}
	}
}
