package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	pb "matchmaker/proto/grpc-server/proto" 
	"google.golang.org/grpc"
)

const (
	totalEntities       = 6                // [Jugador1, Jugador2, Matchmaker, Servidor1, Servidor2, Servidor3]
	matchmakerIndex     = 2                // Índice del Matchmaker en el reloj vectorial
	matchSize           = 2                // Partidas de 1v1, necesitamos 2 jugadores
	matchmakingInterval = 5 * time.Second  // Intentar formar partidas cada 5 segundos
	serverTimeout       = 10 * time.Second // Tiempo máximo que un servidor puede estar OCUPADO
)

// ---- Estructuras de Datos para el Estado Interno ----

type PlayerInfo struct {
	ID        string
	Status    string // "IN_QUEUE", "IN_MATCH"
	QueueTime time.Time
	MatchID   string
}

type ServerInfo struct {
	ID            string
	Address       string
	Status        string // "DISPONIBLE", "OCUPADO", "CAIDO"
	LastHeartbeat time.Time
	MatchID       string
	Client        pb.GameServerClient // Cliente gRPC para comunicarse con este servidor
}

type matchmakerServer struct {
	pb.UnimplementedMatchmakerServer

	playerMu 			sync.RWMutex
	notificationMu		sync.RWMutex
	mu                  sync.RWMutex // Mutex para proteger todas las estructuras de estado
	vectorClock         []int32
	players             map[string]*PlayerInfo
	playerQueue         []string // IDs de jugadores en cola (FIFO)
	servers             map[string]*ServerInfo
	notificationStreams map[string]pb.Matchmaker_StreamMatchNotificationsServer // Streams para notificar a jugadores
}

// ---- Lógica de Relojes Vectoriales ----

func (s *matchmakerServer) incrementClock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vectorClock[matchmakerIndex]++
	log.Printf("[Matchmaker] Reloj incrementado: %v", s.vectorClock)
}

func (s *matchmakerServer) mergeClocks(remoteClock []int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.vectorClock) != len(remoteClock) {
		log.Printf("[Matchmaker] Advertencia: Relojes de diferente tamaño, no se puede fusionar.")
		return
	}
	for i := 0; i < len(s.vectorClock); i++ {
		if remoteClock[i] > s.vectorClock[i] {
			s.vectorClock[i] = remoteClock[i]
		}
	}
	log.Printf("[Matchmaker] Reloj fusionado: %v", s.vectorClock)
}

// ---- Implementación de la Interfaz gRPC ----

// UpdateServerStatus es llamado por los Servidores de Partida
func (s *matchmakerServer) UpdateServerStatus(ctx context.Context, req *pb.ServerStatusUpdateRequest) (*pb.ServerStatusUpdateResponse, error) {
	s.mergeClocks(req.GetVectorClock())
	s.incrementClock() // Evento de recibir una actualización

	s.mu.Lock()
	defer s.mu.Unlock()

	server, exists := s.servers[req.GetServerId()]
	if !exists {
		log.Printf("[Matchmaker] Registrando nuevo servidor: %s escuchando en %s", req.GetServerId(), req.GetAddress())
		// Crear conexión gRPC hacia el nuevo servidor
		conn, err := grpc.Dial(req.GetAddress(), grpc.WithInsecure())
		if err != nil {
			log.Printf("[Matchmaker] No se pudo conectar al nuevo servidor %s: %v", req.GetServerId(), err)
			// Aún lo registramos como CAIDO para que el admin lo vea.
			s.servers[req.GetServerId()] = &ServerInfo{
				ID:      req.GetServerId(),
				Address: req.GetAddress(),
				Status:  "CAIDO",
			}
			return nil, err
		}

		server = &ServerInfo{
			ID:      req.GetServerId(),
			Address: req.GetAddress(),
			Client:  pb.NewGameServerClient(conn),
		}
		s.servers[req.GetServerId()] = server
	}

	log.Printf("[Matchmaker] Estado actualizado para %s: %s", req.GetServerId(), req.GetNewStatus())
	server.Status = req.GetNewStatus()
	server.LastHeartbeat = time.Now()
	if server.Status == "DISPONIBLE" {
		server.MatchID = "" // Limpiar el match ID cuando el servidor queda libre
	}

	return &pb.ServerStatusUpdateResponse{VectorClock: s.vectorClock}, nil
}

// QueuePlayer es llamado por los Jugadores
func (s *matchmakerServer) QueuePlayer(ctx context.Context, req *pb.QueuePlayerRequest) (*pb.QueuePlayerResponse, error) {
	s.mergeClocks(req.GetVectorClock())
	s.incrementClock()

	s.playerMu.Lock()
	defer s.playerMu.Unlock()

	playerID := req.GetPlayerId()
	player, exists := s.players[playerID]

	if !exists {
		player = &PlayerInfo{ID: playerID}
		s.players[playerID] = player
	}

	// Idempotencia: si ya está en cola o en partida, no hacer nada.
	if player.Status == "IN_QUEUE" || player.Status == "IN_MATCH" {
		log.Printf("[Matchmaker] Jugador %s ya está en el sistema (%s).", playerID, player.Status)
		return &pb.QueuePlayerResponse{
			StatusCode:  pb.QueuePlayerResponse_ALREADY_IN_QUEUE,
			Message:     fmt.Sprintf("Ya estás %s", player.Status),
			VectorClock: s.vectorClock,
		}, nil
	}

	player.Status = "IN_QUEUE"
	player.QueueTime = time.Now()
	s.playerQueue = append(s.playerQueue, playerID)
	log.Printf("[Matchmaker] Jugador %s añadido a la cola. Cola actual: %v", playerID, s.playerQueue)

	return &pb.QueuePlayerResponse{
		StatusCode:  pb.QueuePlayerResponse_SUCCESS,
		Message:     "Añadido a la cola de emparejamiento.",
		VectorClock: s.vectorClock,
	}, nil
}

// LeaveQueue es llamado por los Jugadores para abandonar la cola.
func (s *matchmakerServer) LeaveQueue(ctx context.Context, req *pb.PlayerStatusRequest) (*pb.LeaveQueueResponse, error) {
    s.mergeClocks(req.GetVectorClock())
    s.incrementClock() // Es un evento de escritura

    s.playerMu.Lock()
    defer s.playerMu.Unlock()

    playerID := req.GetPlayerId()
    player, exists := s.players[playerID]

    // Caso 1: El jugador no existe o no está en la cola
    if !exists || player.Status != "IN_QUEUE" {
        log.Printf("[Matchmaker] Jugador %s intentó salir de la cola, pero no estaba en ella.", playerID)
        return &pb.LeaveQueueResponse{
            Message:     "No estabas en la cola.",
            VectorClock: s.vectorClock,
        }, nil
    }

    // Caso 2: El jugador está en la cola, lo removemos
    log.Printf("[Matchmaker] Jugador %s ha salido de la cola.", playerID)
    player.Status = "IDLE" // O podrías removerlo del mapa `s.players`
    player.MatchID = ""
    
    // Remover al jugador del slice `playerQueue`
    var newQueue []string
    for _, idInQueue := range s.playerQueue {
        if idInQueue != playerID {
            newQueue = append(newQueue, idInQueue)
        }
    }
    s.playerQueue = newQueue
    delete(s.players, playerID)
    log.Printf("[Matchmaker] Cola actualizada: %v", s.playerQueue)

    return &pb.LeaveQueueResponse{
        Message:     "Has salido de la cola de emparejamiento.",
        VectorClock: s.vectorClock,
    }, nil
}

// GetPlayerStatus es llamado por los Jugadores (Implementa Read-Your-Writes)
func (s *matchmakerServer) GetPlayerStatus(ctx context.Context, req *pb.PlayerStatusRequest) (*pb.PlayerStatusResponse, error) {
	// Para RYW, primero nos aseguramos de que nuestro estado es al menos tan reciente como el del cliente.
	s.mergeClocks(req.GetVectorClock())

	s.playerMu.Lock() // Usamos RLock para permitir lecturas concurrentes
	defer s.playerMu.Unlock()

	player, exists := s.players[req.GetPlayerId()]
	if !exists {
		return &pb.PlayerStatusResponse{
			PlayerStatus: "IDLE",
			VectorClock:  s.vectorClock,
		}, nil
	}

	var serverAddr string
	if player.Status == "IN_MATCH" {
		if server, ok := s.servers[s.players[player.ID].MatchID]; ok {
			serverAddr = server.Address
		}
	}

	return &pb.PlayerStatusResponse{
		PlayerStatus:      player.Status,
		MatchId:           player.MatchID,
		GameServerAddress: serverAddr,
		VectorClock:       s.vectorClock,
	}, nil
}

// StreamMatchNotifications maneja las suscripciones de los jugadores
func (s *matchmakerServer) StreamMatchNotifications(req *pb.PlayerIdRequest, stream pb.Matchmaker_StreamMatchNotificationsServer) error {
	playerID := req.GetPlayerId()
	log.Printf("[Matchmaker] Jugador %s se ha conectado para recibir notificaciones.", playerID)

	s.notificationMu.Lock()
	s.notificationStreams[playerID] = stream
	s.notificationMu.Unlock()

	// Mantener el stream abierto hasta que el cliente se desconecte
	<-stream.Context().Done()

	s.notificationMu.Lock()
	delete(s.notificationStreams, playerID)
	s.notificationMu.Unlock()

	log.Printf("[Matchmaker] Jugador %s se ha desconectado de las notificaciones.", playerID)
	return nil
}

func (s *matchmakerServer) NotifyMatchEnded(ctx context.Context, req *pb.MatchEndedRequest) (*pb.MatchEndedResponse, error) {
	s.mergeClocks(req.GetVectorClock())
	s.incrementClock()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, pid := range req.GetPlayerIds() {
		if player, ok := s.players[pid]; ok {
			player.Status = "IDLE"
			player.MatchID = ""
			log.Printf("[Matchmaker] Jugador %s marcado como IDLE tras fin de partida %s", pid, req.GetMatchId())
		}
	}

	return &pb.MatchEndedResponse{VectorClock: s.vectorClock}, nil
}


// ---- Lógica de Emparejamiento y Detección de Fallos ----
// Bucle de detección de fallos
func (s *matchmakerServer) failureDetectionLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.checkServerTimeouts()
	}
}

func (s *matchmakerServer) attemptToCreateMatch() {
	// Paso 1: Adquirir el lock solo para leer y modificar el estado INTERNO.
	log.Printf("[Matchmaker] Intentado crear partida...")
	time.Sleep(matchmakingInterval)

	if len(s.playerQueue) < matchSize {
		return
	}

	var availableServer *ServerInfo
	for _, server := range s.servers {
		if server.Status == "DISPONIBLE" {
			availableServer = server
			break
		}
	}

	if availableServer == nil {
		return
	}

	// Tenemos jugadores y un servidor.
	s.incrementClock()

	// Copiamos TODOS los datos que necesitamos para la llamada de red.
	playersForMatchIDs := s.playerQueue[:matchSize]
	s.playerQueue = s.playerQueue[matchSize:] // Actualizamos la cola
	
	matchID := fmt.Sprintf("match-%d", time.Now().UnixNano())
	serverToCall := *availableServer // Copia el struct, no el puntero
	
	// Marcar recursos como ocupados INTERNAMENTE
	availableServer.Status = "OCUPADO"
	availableServer.MatchID = matchID
	for _, playerID := range playersForMatchIDs {
		s.players[playerID].Status = "IN_MATCH"
		s.players[playerID].MatchID = matchID
	}
    
    // Obtenemos el reloj vectorial que vamos a enviar
    clockToSend := s.vectorClock

	// Paso 2: Liberar el lock ANTES de la llamada de red.
	log.Printf("[Matchmaker] Cargando partida %s...", matchID)


	// Paso 3: Realizar la llamada de red sin mantener ningún lock.
	// La hacemos en una goroutine para no bloquear el bucle de matchmaking.
	go func(serverCopy ServerInfo, pIDs []string, mID string, vc []int32) {
		log.Printf("[Matchmaker] Goroutine: contactando al servidor %s para la partida %s", serverCopy.ID, mID)
		
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
        
        // Necesitamos la conexión gRPC, que estaba en el struct original.
        // Es mejor tener una función que obtenga la conexión de forma segura.
        // Por ahora, asumimos que `serverCopy.Client` es seguro de usar.

		req := &pb.AssignMatchRequest{
			MatchId:     mID,
			PlayerIds:   pIDs,
			VectorClock: vc,
		}

		_, err := serverCopy.Client.AssignMatch(ctx, req)

		if err != nil {
			log.Printf("[Matchmaker] Fallo al asignar partida %s al servidor %s: %v", mID, serverCopy.ID, err)
			s.handleFailedAssignment(serverCopy.ID, pIDs) // Esta función debe manejar su propio lock.
			return
		}

		// Si tiene éxito, notificamos a los jugadores.
		log.Printf("[Matchmaker] Partida %s asignada exitosamente a %s.", mID, serverCopy.ID)
		s.notifyPlayersOfMatch(pIDs, mID, serverCopy.Address) // Esta función también debe manejar su propio lock.

	}(serverToCall, playersForMatchIDs, matchID, clockToSend)
}

func (s *matchmakerServer) checkServerTimeouts() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, server := range s.servers {
		if server.Status == "OCUPADO" && time.Since(server.LastHeartbeat) > serverTimeout {
			log.Printf("[Matchmaker] Timeout detectado para el servidor %s. Marcando como CAIDO.", server.ID)
			s.incrementClock()
			server.Status = "CAIDO"

			// Devolver jugadores a la cola si estaban en una partida en este servidor
			var playersToRequeue []string
			for playerID, playerInfo := range s.players {
				if playerInfo.MatchID == server.MatchID {
					playersToRequeue = append(playersToRequeue, playerID)
				}
			}
			if len(playersToRequeue) > 0 {
				log.Printf("[Matchmaker] Devolviendo jugadores %v a la cola.", playersToRequeue)
				for _, playerID := range playersToRequeue {
					s.players[playerID].Status = "IN_QUEUE"
					s.players[playerID].MatchID = ""
					s.playerQueue = append([]string{playerID}, s.playerQueue...) // Devolver al inicio de la cola
				}
			}
		}
	}
}

func (s *matchmakerServer) handleFailedAssignment(serverID string, playerIDs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.incrementClock()
	if server, ok := s.servers[serverID]; ok {
		server.Status = "CAIDO"
	}

	log.Printf("[Matchmaker] Devolviendo jugadores %v a la cola debido a fallo de asignación.", playerIDs)
	for _, playerID := range playerIDs {
		s.players[playerID].Status = "IN_QUEUE"
		s.players[playerID].MatchID = ""
	}
	// Añadir jugadores de vuelta al inicio de la cola para que tengan prioridad
	s.playerQueue = append(playerIDs, s.playerQueue...)
}

func (s *matchmakerServer) notifyPlayersOfMatch(playerIDs []string, matchID, serverAddress string) {
	s.notificationMu.RLock()
	defer s.notificationMu.RUnlock()

	notification := &pb.MatchNotification{
		MatchId:           matchID,
		GameServerAddress: serverAddress,
		PlayerIds:         playerIDs,
		VectorClock:       s.vectorClock,
	}

	for _, playerID := range playerIDs {
		if stream, ok := s.notificationStreams[playerID]; ok {
			if err := stream.Send(notification); err != nil {
				log.Printf("[Matchmaker] Error al notificar al jugador %s: %v", playerID, err)
			}
		}
	}
}

// ... Implementación de AdminGetSystemStatus (simplificada)
func (s *matchmakerServer) AdminGetSystemStatus(ctx context.Context, req *pb.AdminRequest) (*pb.SystemStatusResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	resp := &pb.SystemStatusResponse{
		VectorClock: s.vectorClock,
	}
	for _, server := range s.servers {
		resp.Servers = append(resp.Servers, &pb.SystemStatusResponse_ServerState{
			ServerId:       server.ID,
			Status:         server.Status,
			Address:        server.Address,
			CurrentMatchId: server.MatchID,
		})
	}
	for _, playerID := range s.playerQueue {
		resp.PlayerQueue = append(resp.PlayerQueue, &pb.SystemStatusResponse_PlayerQueueEntry{
			PlayerId:           playerID,
			TimeInQueueSeconds: int64(time.Since(s.players[playerID].QueueTime).Seconds()),
		})
	}
	return resp, nil
}

func main() {
	server := &matchmakerServer{
		vectorClock:         make([]int32, totalEntities),
		players:             make(map[string]*PlayerInfo),
		playerQueue:         make([]string, 0),
		servers:             make(map[string]*ServerInfo),
		notificationStreams: make(map[string]pb.Matchmaker_StreamMatchNotificationsServer),
	}

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("Error al escuchar: %v", err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterMatchmakerServer(grpcServer, server)

	go func() {
		for {
			// Iniciar bucles de fondo para la lógica principal
			go server.attemptToCreateMatch()
			//go server.failureDetectionLoop()
			time.Sleep(matchmakingInterval)
		}
	}()

	log.Println("Servidor Matchmaker escuchando en :50051")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Falla al servir gRPC: %v", err)
	}
}
